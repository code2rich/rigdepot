// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package masterkey

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Sealed is a value sealed with envelope encryption. Tables that keep the
// parts in their own columns (secret_values) store the fields; tables with
// one column for it store Encode.
type Sealed struct {
	KeyID      string // the master key that wrapped DataKey
	WrappedKey []byte // the record's data key, wrapped: nonce ‖ AES-GCM ciphertext
	Nonce      []byte // of the value's encryption
	Ciphertext []byte // the value under the data key, with the GCM tag
}

var (
	// ErrUnknownKey means the record was sealed under a master key that is
	// not in the keyring: the key file is not the one the database was
	// encrypted with, or a retired key was removed too early.
	ErrUnknownKey = errors.New("masterkey: sealed under a master key that is not loaded")
	// ErrOpen means the record does not open: it was changed, or it is
	// opened with other additional data than it was sealed with (moved to
	// another owner).
	ErrOpen = errors.New("masterkey: the sealed value does not open")
)

const (
	dataKeySize = 32
	nonceSize   = 12
)

// Seal encrypts plaintext under a fresh data key and wraps the data key with
// the current master key. ad is the additional data that binds the value to
// where it belongs, such as "secret_values/<user>/<name>/<version>"; Open
// needs the same.
func (k *Keyring) Seal(plaintext, ad []byte) (Sealed, error) {
	dek := make([]byte, dataKeySize)
	if _, err := rand.Read(dek); err != nil {
		return Sealed{}, err
	}
	nonce, ct, err := encrypt(dek, plaintext, ad)
	if err != nil {
		return Sealed{}, err
	}
	cur := k.current()
	wrapped, err := wrap(cur, dek)
	if err != nil {
		return Sealed{}, err
	}
	return Sealed{KeyID: cur.id, WrappedKey: wrapped, Nonce: nonce, Ciphertext: ct}, nil
}

// Open decrypts a sealed value. ad must be what it was sealed with.
func (k *Keyring) Open(s Sealed, ad []byte) ([]byte, error) {
	dek, err := k.unwrap(s)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	if len(s.Nonce) != nonceSize {
		return nil, ErrOpen
	}
	out, err := gcm.Open(nil, s.Nonce, s.Ciphertext, ad)
	if err != nil {
		return nil, ErrOpen
	}
	return out, nil
}

// Rewrap wraps the value's data key with the current master key. The value's
// ciphertext stays as it is, so nobody has to enter anything again. It
// reports false, and returns s unchanged, when s already uses the current
// key.
func (k *Keyring) Rewrap(s Sealed) (Sealed, bool, error) {
	cur := k.current()
	if s.KeyID == cur.id {
		return s, false, nil
	}
	dek, err := k.unwrap(s)
	if err != nil {
		return s, false, err
	}
	wrapped, err := wrap(cur, dek)
	if err != nil {
		return s, false, err
	}
	s.KeyID, s.WrappedKey = cur.id, wrapped
	return s, true, nil
}

func (k *Keyring) unwrap(s Sealed) ([]byte, error) {
	mk, ok := k.find(s.KeyID)
	if !ok {
		return nil, fmt.Errorf("%w (key %s; loaded: %s)", ErrUnknownKey, s.KeyID, strings.Join(k.IDs(), ", "))
	}
	if len(s.WrappedKey) < nonceSize {
		return nil, ErrOpen
	}
	gcm, err := newGCM(mk.kek)
	if err != nil {
		return nil, err
	}
	dek, err := gcm.Open(nil, s.WrappedKey[:nonceSize], s.WrappedKey[nonceSize:], wrapAD(mk.id))
	if err != nil || len(dek) != dataKeySize {
		return nil, ErrOpen
	}
	return dek, nil
}

func wrap(mk key, dek []byte) ([]byte, error) {
	nonce, ct, err := encrypt(mk.kek, dek, wrapAD(mk.id))
	if err != nil {
		return nil, err
	}
	return append(nonce, ct...), nil
}

// wrapAD binds a wrapped data key to the master key that wrapped it.
func wrapAD(keyID string) []byte { return []byte("data key/" + keyID) }

func encrypt(key, plaintext, ad []byte) (nonce, ct []byte, err error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, gcm.Seal(nil, nonce, plaintext, ad), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealedPrefix starts Encode's form, so that a format change can be told
// apart later.
const sealedPrefix = "mk1"

// Encode returns s as one line of text, for a single column:
// mk1.<key id>.<wrapped key>.<nonce>.<ciphertext>, binary parts in
// unpadded URL-safe base64.
func (s Sealed) Encode() string {
	enc := base64.RawURLEncoding
	return strings.Join([]string{sealedPrefix, s.KeyID, enc.EncodeToString(s.WrappedKey),
		enc.EncodeToString(s.Nonce), enc.EncodeToString(s.Ciphertext)}, ".")
}

// Decode parses what Encode returned.
func Decode(text string) (Sealed, error) {
	parts := strings.Split(text, ".")
	if len(parts) != 5 || parts[0] != sealedPrefix || parts[1] == "" {
		return Sealed{}, errors.New("masterkey: not a sealed value")
	}
	enc := base64.RawURLEncoding
	var s Sealed
	var err error
	s.KeyID = parts[1]
	if s.WrappedKey, err = enc.DecodeString(parts[2]); err != nil {
		return Sealed{}, errors.New("masterkey: not a sealed value")
	}
	if s.Nonce, err = enc.DecodeString(parts[3]); err != nil {
		return Sealed{}, errors.New("masterkey: not a sealed value")
	}
	if s.Ciphertext, err = enc.DecodeString(parts[4]); err != nil {
		return Sealed{}, errors.New("masterkey: not a sealed value")
	}
	return s, nil
}
