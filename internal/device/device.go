// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package device

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/keyring"
)

// The failures of this package besides the server's (*apiclient.Error) and
// the network's (apiclient.Unreachable).
var (
	// ErrNotLoggedIn: there is no device token on this computer, or it was
	// issued by another server than the one asked about.
	ErrNotLoggedIn = errors.New("device: not signed in")
	// ErrDenied: the person refused the sign-in in the browser.
	ErrDenied = errors.New("device: the sign-in was refused in the browser")
	// ErrExpired: nobody approved the sign-in in time.
	ErrExpired = errors.New("device: the sign-in request expired")
)

// StoreError is a failure of the keychain other than "not there": the
// device key or token cannot be read or written right now. What is stored is
// then unknown, so nothing is deleted and no new key pair is made: that would
// make every secret the server sealed to the old key useless.
type StoreError struct {
	Entry string // keyring.DeviceKey or keyring.DeviceToken
	Err   error
}

func (e *StoreError) Error() string {
	return fmt.Sprintf("device: the keychain entry %s: %v", e.Entry, e.Err)
}
func (e *StoreError) Unwrap() error { return e.Err }

// AlreadyLoggedInError: this computer is signed in already. Signing in again
// needs signing out first, so that the old device does not stay signed in
// on the server without anyone holding its token.
type AlreadyLoggedInError struct{ Credentials Credentials }

func (e *AlreadyLoggedInError) Error() string {
	return "device: already signed in to " + e.Credentials.Server + " as " + e.Credentials.Username
}

// Credentials are what this computer keeps after signing in: the device
// token, and what it belongs to. They are one keychain entry
// (keyring.DeviceToken), as JSON.
type Credentials struct {
	// Server is the server that issued the token, as apiclient.NormalServer
	// writes it. The token is sent to this server and no other.
	Server   string `json:"server"`
	DeviceID string `json:"device_id"`
	Token    string `json:"token"`
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	// DisplayName is the person's name when they signed in.
	DisplayName string `json:"display_name,omitempty"`
}

// Identity is this computer's device identity: its X25519 key pair and its
// device token, kept in a keyring.Store.
//
// The private key is the one the server seals personal secrets to (HPKE,
// docs/architecture/06-server.md#密钥加密): X25519, as crypto/hpke's
// DHKEM(X25519, HKDF-SHA256) needs it. It is stored as its 32 raw bytes.
type Identity struct {
	store keyring.Store
}

// New returns the identity kept in store.
func New(store keyring.Store) *Identity { return &Identity{store: store} }

// Store returns where the identity is kept.
func (id *Identity) Store() keyring.Store { return id.store }

// Credentials returns the sign-in of this computer: ErrNotLoggedIn when
// there is none, a *StoreError when it cannot be told.
func (id *Identity) Credentials() (Credentials, error) {
	raw, err := id.store.Get(keyring.DeviceToken)
	if errors.Is(err, keyring.ErrNotFound) {
		return Credentials{}, ErrNotLoggedIn
	}
	if err != nil {
		return Credentials{}, &StoreError{Entry: keyring.DeviceToken, Err: err}
	}
	var c Credentials
	if err := json.Unmarshal(raw, &c); err != nil || c.Token == "" || c.Server == "" || c.DeviceID == "" {
		if err == nil {
			err = errors.New("incomplete")
		}
		return Credentials{}, &StoreError{Entry: keyring.DeviceToken, Err: fmt.Errorf("cannot be read: %w", err)}
	}
	return c, nil
}

// CredentialsFor returns the sign-in of this computer for server, and
// ErrNotLoggedIn when the computer is signed in to another server or not at
// all: a token goes only to the server that issued it.
func (id *Identity) CredentialsFor(server string) (Credentials, error) {
	c, err := id.Credentials()
	if err != nil {
		return c, err
	}
	want, err := apiclient.NormalServer(server)
	if err != nil || want != c.Server {
		return Credentials{}, ErrNotLoggedIn
	}
	return c, nil
}

// Connect returns a client for the server this computer is signed in to,
// with its device token; o.Server and o.Token are set from the credentials.
func (id *Identity) Connect(o apiclient.Options) (*apiclient.ClientWithResponses, Credentials, error) {
	c, err := id.Credentials()
	if err != nil {
		return nil, c, err
	}
	o.Server, o.Token = c.Server, c.Token
	client, err := apiclient.Connect(o)
	return client, c, err
}

// PrivateKey returns the device's private key: ErrNotLoggedIn when there is
// none, a *StoreError when it cannot be read.
func (id *Identity) PrivateKey() (*ecdh.PrivateKey, error) {
	raw, err := id.store.Get(keyring.DeviceKey)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, &StoreError{Entry: keyring.DeviceKey, Err: err}
	}
	k, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return nil, &StoreError{Entry: keyring.DeviceKey, Err: fmt.Errorf("not an X25519 key: %w", err)}
	}
	return k, nil
}

// ensureKey returns the device's private key, making and storing a new one
// only when the keychain says there is none. Any other answer stops: a key
// that cannot be read now is still there.
func (id *Identity) ensureKey() (*ecdh.PrivateKey, error) {
	k, err := id.PrivateKey()
	if !errors.Is(err, ErrNotLoggedIn) {
		return k, err
	}
	k, err = ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := id.store.Set(keyring.DeviceKey, k.Bytes()); err != nil {
		return nil, &StoreError{Entry: keyring.DeviceKey, Err: err}
	}
	return k, nil
}

// PublicKey is how the contract sends a device's public key: 32 bytes in
// unpadded base64url.
func PublicKey(k *ecdh.PrivateKey) string {
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
}

// KeyStorage is where the device key lives, as the contract says it:
// "keyring" or "file".
func (id *Identity) KeyStorage() string {
	if id.store.Kind().IsFile() {
		return "file"
	}
	return "keyring"
}

// forget deletes the device token, then the device key. The token goes
// first: a computer with a key and no token is signed out, one with a token
// and no key would not be able to open its secrets.
func (id *Identity) forget() error {
	if err := id.store.Delete(keyring.DeviceToken); err != nil {
		return &StoreError{Entry: keyring.DeviceToken, Err: err}
	}
	if err := id.store.Delete(keyring.DeviceKey); err != nil {
		return &StoreError{Entry: keyring.DeviceKey, Err: err}
	}
	return nil
}
