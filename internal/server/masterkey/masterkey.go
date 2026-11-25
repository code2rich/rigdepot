// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package masterkey

import (
	"bufio"
	"bytes"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
)

// KeySize is the length of a master key in bytes.
const KeySize = 32

// The HKDF labels. They are fixed strings on purpose: deriving them from the
// product's name would make every stored secret unreadable if the name ever
// changed.
const (
	infoKEK     = "envelope key-encryption key v1"
	infoKeyID   = "master key id v1"
	infoPurpose = "purpose v1: "
)

// Source is where the master key came from.
type Source int

// The sources of docs/architecture/08-deployment.md#配置, in the order Load
// tries them.
const (
	FromFile      Source = iota + 1 // RGD_MASTER_KEY_FILE, or the default file in the data directory
	FromEnv                         // RGD_MASTER_KEY
	FromGenerated                   // generated into the default file on this start
)

func (s Source) String() string {
	switch s {
	case FromFile:
		return "file"
	case FromEnv:
		return "environment"
	case FromGenerated:
		return "generated"
	}
	return fmt.Sprintf("Source(%d)", int(s))
}

// ErrNoKeyFile is returned by Load when the key file does not exist and may
// not be generated.
var ErrNoKeyFile = errors.New("masterkey: the master key file does not exist")

// key is one master key and what is derived from it.
type key struct {
	id     string // KeyID
	secret []byte
	kek    []byte
}

func newKey(secret []byte) (key, error) {
	if len(secret) != KeySize {
		return key{}, fmt.Errorf("masterkey: a master key is %d bytes, not %d", KeySize, len(secret))
	}
	kek, err := hkdf.Key(sha256.New, secret, nil, infoKEK, 32)
	if err != nil {
		return key{}, err
	}
	idBytes, err := hkdf.Key(sha256.New, secret, nil, infoKeyID, 8)
	if err != nil {
		return key{}, err
	}
	return key{id: hex.EncodeToString(idBytes), secret: bytes.Clone(secret), kek: kek}, nil
}

// Keyring is the loaded master keys: the current one, which seals, and the
// retired ones kept from earlier rotations, which only open. It is safe for
// concurrent use.
//
// A keyring loaded from a file reloads that file when asked to open a record
// sealed under a key it does not know, so that a server keeps working while
// "keys rotate" runs next to it.
type Keyring struct {
	mu      sync.RWMutex
	keys    []key // keys[0] is current
	source  Source
	path    string    // the file, for FromFile and FromGenerated
	checked time.Time // last reload attempt
}

// LoadOptions says where Load looks.
type LoadOptions struct {
	// Key is the key given directly (RGD_MASTER_KEY), or nil.
	Key []byte
	// File is the key file (RGD_MASTER_KEY_FILE or its default). Used when
	// Key is nil.
	File string
	// Generate allows creating File with a new key when it does not exist.
	// The server allows it only on a database that holds nothing sealed
	// yet, so that a key file lost from its volume is noticed instead of
	// silently replaced.
	Generate bool
}

// Load returns the keyring: from opts.Key when set, else from opts.File,
// generating that file (mode 0600) when it is missing and opts.Generate
// allows it.
func Load(opts LoadOptions) (*Keyring, error) {
	if opts.Key != nil {
		k, err := newKey(opts.Key)
		if err != nil {
			return nil, err
		}
		return &Keyring{keys: []key{k}, source: FromEnv}, nil
	}
	if opts.File == "" {
		return nil, errors.New("masterkey: no key and no key file given")
	}
	keys, err := readFile(opts.File)
	if err == nil {
		return &Keyring{keys: keys, source: FromFile, path: opts.File}, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if !opts.Generate {
		return nil, fmt.Errorf("%w: %s", ErrNoKeyFile, opts.File)
	}
	secret := make([]byte, KeySize)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	k, err := newKey(secret)
	if err != nil {
		return nil, err
	}
	if err := writeNewFile(opts.File, []key{k}); err != nil {
		return nil, err
	}
	return &Keyring{keys: []key{k}, source: FromGenerated, path: opts.File}, nil
}

// New returns a keyring of the given keys, the first one current. Tests and
// tools use it; the server uses Load.
func New(secrets ...[]byte) (*Keyring, error) {
	if len(secrets) == 0 {
		return nil, errors.New("masterkey: no keys")
	}
	keys := make([]key, len(secrets))
	for i, s := range secrets {
		k, err := newKey(s)
		if err != nil {
			return nil, err
		}
		keys[i] = k
	}
	return &Keyring{keys: keys, source: FromEnv}, nil
}

// Source tells where the current key came from.
func (k *Keyring) Source() Source {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.source
}

// Path is the key file, or "" when the key was given directly.
func (k *Keyring) Path() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.path
}

// CurrentID is the identifier of the key that seals. Records store it, so
// that a rotation knows which ones are done. It reveals nothing about the
// key.
func (k *Keyring) CurrentID() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.keys[0].id
}

// IDs are the identifiers of every key in the ring, the current one first.
func (k *Keyring) IDs() []string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	ids := make([]string, len(k.keys))
	for i, key := range k.keys {
		ids[i] = key.id
	}
	return ids
}

// Derive returns a 32-byte key for one purpose, such as the encryption of
// the sign-in state cookie, derived from the current master key. Different
// purposes get unrelated keys. A rotation changes them.
func (k *Keyring) Derive(purpose string) []byte {
	if purpose == "" {
		panic("masterkey: Derive needs a purpose")
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	out, err := hkdf.Key(sha256.New, k.keys[0].secret, nil, infoPurpose+purpose, 32)
	if err != nil {
		panic(err) // only for lengths HKDF cannot produce
	}
	return out
}

func (k *Keyring) current() key {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.keys[0]
}

// find returns the key with the given identifier. When the keyring does not
// have it and came from a file, it reloads the file once (at most every few
// seconds): another process may have rotated the key.
func (k *Keyring) find(id string) (key, bool) {
	k.mu.RLock()
	for _, key := range k.keys {
		if key.id == id {
			k.mu.RUnlock()
			return key, true
		}
	}
	path, stale := k.path, time.Since(k.checked) > reloadInterval
	k.mu.RUnlock()
	if path == "" || !stale {
		return key{}, false
	}
	k.mu.Lock()
	k.checked = time.Now()
	k.mu.Unlock()
	keys, err := readFile(path)
	if err != nil {
		return key{}, false
	}
	k.mu.Lock()
	k.keys = keys
	k.mu.Unlock()
	for _, key := range keys {
		if key.id == id {
			return key, true
		}
	}
	return key{}, false
}

// reloadInterval keeps a burst of records sealed under an unknown key from
// rereading the key file for each of them.
const reloadInterval = 2 * time.Second

// The key file: lines of standard base64, one key each, the current key
// first; blank lines and lines starting with # are ignored. A file of
// exactly 32 bytes that is not that is taken as the raw key.

func readFile(path string) ([]key, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("masterkey: read the key file: %w", err)
	}
	keys, err := parseFile(data)
	if err != nil {
		return nil, fmt.Errorf("masterkey: %s: %w", path, err)
	}
	return keys, nil
}

func parseFile(data []byte) ([]key, error) {
	if len(data) == KeySize {
		// A key line is 44 characters, so a 32-byte file can only be a raw key.
		k, err := newKey(data)
		if err != nil {
			return nil, err
		}
		return []key{k}, nil
	}
	var keys []key
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		secret, err := base64.StdEncoding.DecodeString(text)
		if err != nil || len(secret) != KeySize {
			// The line may be the secret itself: never quote it.
			return nil, fmt.Errorf("line %d is not a %d-byte key in standard base64", line, KeySize)
		}
		k, err := newKey(secret)
		if err != nil {
			return nil, err
		}
		if !seen[k.id] {
			seen[k.id] = true
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("the key file holds no key")
	}
	return keys, nil
}

func formatFile(keys []key) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Master key of %s. Back it up, and keep the copy apart from the database backups:\n", brand.Server)
	b.WriteString("# without it nothing encrypted in the database can be read. The first key encrypts;\n")
	b.WriteString("# the keys after it are retired by \"keys rotate\" and only decrypt older records.\n")
	for _, k := range keys {
		b.WriteString(base64.StdEncoding.EncodeToString(k.secret))
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// writeNewFile creates path with the keys, mode 0600, failing when it exists.
func writeNewFile(path string, keys []key) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("masterkey: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("masterkey: create the key file: %w", err)
	}
	if _, err := f.Write(formatFile(keys)); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("masterkey: write the key file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("masterkey: write the key file: %w", err)
	}
	return f.Close()
}

// replaceFile writes the keys to path through a temporary file in the same
// directory and a rename, so that a crash leaves either the old file or the
// new one, never half of one.
func replaceFile(path string, keys []key) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("masterkey: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name) // after the rename there is nothing left to remove
	if err := tmp.Chmod(0o600); err != nil && !isUnsupported(err) {
		tmp.Close()
		return fmt.Errorf("masterkey: %w", err)
	}
	if _, err := tmp.Write(formatFile(keys)); err != nil {
		tmp.Close()
		return fmt.Errorf("masterkey: write the key file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("masterkey: write the key file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("masterkey: write the key file: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("masterkey: replace the key file: %w", err)
	}
	return nil
}

// isUnsupported reports a chmod that the platform does not do (Windows keeps
// only the read-only bit).
func isUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported)
}
