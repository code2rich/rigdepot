// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package localsecrets

import (
	"cmp"
	"crypto/ecdh"
	"crypto/hpke"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/home"
)

// FileName is the vault's file in home.SecretsDir.
const FileName = "vault.json"

// formatVersion is the version of the file this package writes.
const formatVersion = 1

// Suite is the HPKE cipher suite the values are sealed with, as the contract names it.
const Suite = "hpke-x25519-sha256-aes256gcm"

var (
	// ErrUnknown: the vault cannot be read or parsed, or is of a newer format. What it holds is
	// unknown.
	ErrUnknown = errors.New("localsecrets: the local copies of the secrets cannot be read")
	// ErrNotYours: the copies were sealed for another device or person than the one signed in
	// now; a sync replaces them.
	ErrNotYours = errors.New("localsecrets: the local copies of the secrets belong to another sign-in")
	// ErrOpen: a copy does not open with this device's key.
	ErrOpen = errors.New("localsecrets: a local copy does not open with this computer's device key")
)

// MissingError names the secrets that have no local copy.
type MissingError struct{ Names []string }

func (e *MissingError) Error() string {
	return "localsecrets: no local copy of " + strings.Join(e.Names, ", ")
}

// Item is one sealed value, as the server sent it.
type Item struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	// Enc is the HPKE encapsulated key and Ciphertext the sealed value, both base64url.
	Enc        string     `json:"enc"`
	Ciphertext string     `json:"ciphertext"`
	Info       string     `json:"info"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
}

// File is the content of vault.json.
type File struct {
	Schema int `json:"schema"`
	// Server, UserID and DeviceID are the sign-in the values were sealed for.
	Server   string    `json:"server"`
	UserID   string    `json:"userId"`
	DeviceID string    `json:"deviceId"`
	Suite    string    `json:"suite"`
	Updated  time.Time `json:"updated"`
	Items    []Item    `json:"items"`
}

// Find returns the item with the given name.
func (f *File) Find(name string) (Item, bool) {
	i := slices.IndexFunc(f.Items, func(it Item) bool { return it.Name == name })
	if i < 0 {
		return Item{}, false
	}
	return f.Items[i], true
}

// Owner is the sign-in the vault is for.
type Owner struct {
	Server, UserID, DeviceID string
}

// Vault is the vault file of one local data directory.
type Vault struct{ data home.Dir }

// New returns the vault of the local data directory.
func New(data home.Dir) *Vault { return &Vault{data: data} }

// Path returns the file's path.
func (v *Vault) Path() string { return v.data.Join(home.SecretsDir, FileName) }

// Read returns the vault. A vault that does not exist is an empty File; one that cannot be read
// is ErrUnknown.
func (v *Vault) Read() (File, error) {
	data, err := os.ReadFile(v.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return File{Schema: formatVersion}, nil
	}
	if err != nil {
		return File{}, fmt.Errorf("%w: %v", ErrUnknown, err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("%w: %v", ErrUnknown, err)
	}
	if f.Schema < 1 || f.Schema > formatVersion {
		return File{}, fmt.Errorf("%w: format %d", ErrUnknown, f.Schema)
	}
	return f, nil
}

// write replaces the file as a whole.
func (v *Vault) write(f File) error {
	if _, err := v.data.EnsureSub(home.SecretsDir); err != nil {
		return err
	}
	f.Schema, f.Suite = formatVersion, Suite
	slices.SortFunc(f.Items, func(a, b Item) int { return cmp.Compare(a.Name, b.Name) })
	if f.Items == nil {
		f.Items = []Item{}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return home.WriteFileAtomic(v.Path(), append(data, '\n'), home.FilePerm)
}

// Replace makes items the whole vault for owner: what the server sent for every secret of the
// person. Copies of secrets not among items are dropped (deleted on the server).
func (v *Vault) Replace(owner Owner, items []Item, now time.Time) error {
	if _, err := v.Read(); err != nil && !errors.Is(err, ErrUnknown) {
		return err
	}
	// An unreadable vault is replaced as a whole here on purpose: everything in it would come
	// from the same answer of the server.
	return v.write(File{Server: owner.Server, UserID: owner.UserID, DeviceID: owner.DeviceID, Updated: now.UTC(), Items: items})
}

// Update replaces the copies of the named secrets with items, and drops the named ones items
// does not have. A vault of another sign-in is replaced as a whole; an unreadable one is left
// as it is (ErrUnknown).
func (v *Vault) Update(owner Owner, names []string, items []Item, now time.Time) error {
	f, err := v.Read()
	if err != nil {
		return err
	}
	if f.Server != owner.Server || f.UserID != owner.UserID || f.DeviceID != owner.DeviceID {
		f = File{Server: owner.Server, UserID: owner.UserID, DeviceID: owner.DeviceID}
	}
	f.Items = slices.DeleteFunc(f.Items, func(it Item) bool { return slices.Contains(names, it.Name) })
	f.Items = append(f.Items, items...)
	f.Updated = now.UTC()
	return v.write(f)
}

// Remove drops the copies of the named secrets.
func (v *Vault) Remove(names ...string) error {
	f, err := v.Read()
	if err != nil {
		return err
	}
	n := len(f.Items)
	f.Items = slices.DeleteFunc(f.Items, func(it Item) bool { return slices.Contains(names, it.Name) })
	if len(f.Items) == n {
		return nil
	}
	return v.write(f)
}

// Delete removes the vault file: signing out makes the copies useless, since they were sealed
// to the device key that goes with it.
func (v *Vault) Delete() error {
	err := os.Remove(v.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Info is the HPKE info a value sealed for this device carries (the server's secrets.Info).
func Info(userID, deviceID, name string, version int) string {
	return strings.Join([]string{brand.CLI + "/secret/v1", userID, deviceID, name, strconv.Itoa(version)}, "|")
}

// Open unseals the named secrets with the device's private key, for owner. It fails with a
// *MissingError naming every secret that has no copy, before it opens anything.
func (v *Vault) Open(key *ecdh.PrivateKey, owner Owner, names []string) (map[string][]byte, error) {
	f, err := v.Read()
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, n := range names {
		if _, ok := f.Find(n); !ok && !slices.Contains(missing, n) {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, &MissingError{Names: missing}
	}
	if len(names) == 0 {
		return map[string][]byte{}, nil
	}
	if f.UserID != owner.UserID || f.DeviceID != owner.DeviceID || (owner.Server != "" && f.Server != owner.Server) {
		return nil, ErrNotYours
	}
	priv, err := hpke.NewDHKEMPrivateKey(key)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(names))
	for _, n := range names {
		it, _ := f.Find(n)
		v, err := openItem(priv, owner, it)
		if err != nil {
			for _, x := range out {
				clear(x)
			}
			return nil, fmt.Errorf("%w: %s: %v", ErrOpen, n, err)
		}
		out[n] = v
	}
	return out, nil
}

func openItem(priv hpke.PrivateKey, owner Owner, it Item) ([]byte, error) {
	if it.Info != Info(owner.UserID, owner.DeviceID, it.Name, it.Version) {
		return nil, errors.New("it was sealed for another person, computer, name or version")
	}
	enc, err := base64.RawURLEncoding.DecodeString(it.Enc)
	if err != nil {
		return nil, err
	}
	ct, err := base64.RawURLEncoding.DecodeString(it.Ciphertext)
	if err != nil {
		return nil, err
	}
	r, err := hpke.NewRecipient(enc, priv, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(it.Info))
	if err != nil {
		return nil, err
	}
	return r.Open(nil, ct)
}
