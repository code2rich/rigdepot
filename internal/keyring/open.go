// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package keyring

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/home"
)

// MarkerFile is the file in the secrets directory that records where the entries are kept:
// "system" or "file". It is written before the first value and removed when the last entry is.
const MarkerFile = "keyring"

const (
	markerSystem = "system"
	markerFile   = "file"
)

// probeName is the entry looked up to find out whether the system keychain answers. Nothing is
// ever stored under it.
const probeName = "probe"

// Options adjust Open.
type Options struct {
	// Service is the service name of the entries in the system keychain. "" means the long name
	// of the program, with a suffix derived from the path of the local data directory when that
	// is not at its default place, so that two local data directories never share entries.
	Service string
	// Timeout limits each call to the system keychain; zero means DefaultTimeout.
	Timeout time.Duration
	// OnFallback is called when a value is first stored in a file because there is no system
	// keychain. The command uses it to tell the person once, at that moment.
	OnFallback func(Fallback)

	backend backend // the system keychain; nil is the real one. Tests set it.
}

// Fallback describes a store that keeps its entries in files.
type Fallback struct {
	// Kind is PlainFile, or Protected on Windows.
	Kind Kind
	// Dir is the directory of the files.
	Dir string
	// Reason says why the system keychain is not used.
	Reason error
}

// Keyring is the Store programs use: the system keychain when there is one, files otherwise. Its
// methods are safe for concurrent use.
type Keyring struct {
	sys        systemStore
	file       fileStore
	onFallback func(Fallback)

	mu       sync.Mutex
	kind     Kind   // Unknown until decided
	recorded string // what the marker says, or ""
	reason   error  // why the system keychain is not used
}

var _ Store = (*Keyring)(nil)

// Open returns the store of the local data directory dir. It does not touch the keychain or the
// files until it is used.
func Open(dir home.Dir, opts Options) *Keyring {
	b := opts.backend
	if b == nil {
		b = goKeyring{}
	}
	service := opts.Service
	if service == "" {
		service = serviceFor(dir)
	}
	return &Keyring{
		sys:        systemStore{b: b, service: service, timeout: opts.Timeout},
		file:       fileStore{dir: dir},
		onFallback: opts.OnFallback,
	}
}

// serviceFor returns the service name for the local data directory dir.
func serviceFor(dir home.Dir) string {
	if h, err := os.UserHomeDir(); err == nil && filepath.Join(h, brand.Home) == dir.Path() {
		return brand.CLILong
	}
	sum := sha256.Sum256([]byte(dir.Path()))
	return brand.CLILong + "-" + hex.EncodeToString(sum[:4])
}

// Get returns the value of the entry; see Store.
func (k *Keyring) Get(name string) ([]byte, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.resolve(); err != nil {
		return nil, err
	}
	if k.kind == System {
		v, err := k.sys.get(name)
		return v, systemErr(err)
	}
	return k.file.get(name)
}

// Set stores the value; see Store. The first value stored records where the entries are kept.
func (k *Keyring) Set(name string, value []byte) error {
	if err := checkName(name); err != nil {
		return err
	}
	if err := checkValue(value); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.resolve(); err != nil {
		return err
	}
	first := k.recorded == ""
	if first {
		word := markerSystem
		if k.kind.IsFile() {
			word = markerFile
		}
		if err := k.writeMarker(word); err != nil {
			return err
		}
		k.recorded = word
	}
	if k.kind == System {
		return systemErr(k.sys.set(name, value))
	}
	if err := k.file.set(name, value); err != nil {
		return err
	}
	if first && k.onFallback != nil {
		k.onFallback(Fallback{Kind: k.kind, Dir: k.secretsDir(), Reason: k.reason})
	}
	return nil
}

// Delete removes the entry; see Store. When no entry is left, the record of where they were kept
// goes too, so that the next value stored goes to the system keychain if there is one by then.
func (k *Keyring) Delete(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.resolve(); err != nil {
		return err
	}
	var err error
	if k.kind == System {
		err = systemErr(k.sys.delete(name))
	} else {
		err = k.file.delete(name)
	}
	if err != nil {
		return err
	}
	return k.forgetIfEmpty()
}

// Kind says where the entries are kept; Unknown when that cannot be told right now.
func (k *Keyring) Kind() Kind {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.resolve() != nil {
		return Unknown
	}
	return k.kind
}

// Status is what Probe found.
type Status struct {
	// Kind is where the entries are kept, or would be if one were stored now; Unknown when that
	// cannot be told.
	Kind Kind
	// Recorded is true when the choice is recorded, that is, something was stored.
	Recorded bool
	// Dir is the directory of the fallback files.
	Dir string
	// Reason says why the system keychain is not used, when Kind is a file and that was found
	// out just now.
	Reason error
	// Err is set when the entries cannot be reached: the system keychain that holds them does
	// not answer (matching ErrUnavailable or not), or the files cannot be looked at.
	Err error
	// Insecure are the fallback files that are not used because others can open them.
	Insecure []string
}

// Probe finds out where the entries are kept and whether they can be reached, for the self-check.
// It reads and changes nothing that is stored.
func (k *Keyring) Probe() Status {
	k.mu.Lock()
	defer k.mu.Unlock()
	st := Status{Dir: k.secretsDir()}
	kind, recorded, reason, err := k.where()
	st.Recorded = recorded != ""
	if err != nil {
		st.Err = err
		return st
	}
	st.Kind, st.Reason = kind, reason
	if kind.IsFile() {
		st.Insecure, st.Err = k.file.insecure()
		return st
	}
	if recorded != "" {
		// where trusted the record; look whether the keychain answers.
		if _, err := k.sys.get(probeName); err != nil && !errors.Is(err, ErrNotFound) {
			st.Err = systemErr(err)
		}
	}
	return st
}

// resolve decides where the entries are kept, once.
func (k *Keyring) resolve() error {
	if k.kind != Unknown {
		return nil
	}
	kind, recorded, reason, err := k.where()
	if err != nil {
		return err
	}
	k.kind, k.recorded, k.reason = kind, recorded, reason
	return nil
}

// where finds out where the entries are kept without changing anything: where the record says;
// in files when there are entry files but no record; otherwise in the system keychain if it
// answers, and in files if there is none. A keychain that is there and fails is an error.
func (k *Keyring) where() (kind Kind, recorded string, reason error, err error) {
	if k.file.dir.Path() == "" {
		return Unknown, "", nil, errors.New("keyring: the local data directory is not known")
	}
	recorded, err = k.readMarker()
	if err != nil {
		return Unknown, "", nil, err
	}
	switch recorded {
	case markerSystem:
		return System, recorded, nil, nil
	case markerFile:
		return fileKind, recorded, nil, nil
	}
	found, err := k.file.entries()
	if err != nil {
		return Unknown, "", nil, err
	}
	if len(found) > 0 {
		return fileKind, "", nil, nil
	}
	_, err = k.sys.get(probeName)
	switch {
	case err == nil, errors.Is(err, ErrNotFound):
		return System, "", nil, nil
	case unavailable(err):
		return fileKind, "", err, nil
	}
	return Unknown, "", nil, err
}

// forgetIfEmpty removes the record when no entry is left. When that cannot be told, the record
// stays.
func (k *Keyring) forgetIfEmpty() error {
	if k.recorded == "" {
		return nil
	}
	if k.kind == System {
		for _, name := range names {
			if _, err := k.sys.get(name); !errors.Is(err, ErrNotFound) {
				return nil
			}
		}
	} else if found, err := k.file.entries(); err != nil || len(found) > 0 {
		return nil
	}
	err := os.Remove(k.markerPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	k.kind, k.recorded, k.reason = Unknown, "", nil
	return nil
}

func (k *Keyring) secretsDir() string { return k.file.dir.Join(home.SecretsDir) }

func (k *Keyring) markerPath() string { return k.file.dir.Join(home.SecretsDir, MarkerFile) }

// readMarker returns the word in the marker, or "" when there is none. A marker that says
// anything else is an error: where the entries are is then not known.
func (k *Keyring) readMarker() (string, error) {
	p := k.markerPath()
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	switch w := string(bytes.TrimSpace(data)); w {
	case markerSystem, markerFile:
		return w, nil
	}
	return "", fmt.Errorf("keyring: %s does not say %q or %q", p, markerSystem, markerFile)
}

func (k *Keyring) writeMarker(word string) error {
	if _, err := k.file.dir.EnsureSub(home.SecretsDir); err != nil {
		return err
	}
	return home.WriteFileAtomic(k.markerPath(), []byte(word+"\n"), home.FilePerm)
}

// systemErr makes an error that says the system keychain is not there match ErrUnavailable:
// the entries are kept there, so it is an unknown, not a reason to look elsewhere.
func systemErr(err error) error {
	if unavailable(err) {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return err
}
