// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package keyring

import (
	"errors"
	"fmt"
)

// Names of the entries. In the file fallback they are also the file names in the secrets
// directory, as the layout of the local data directory lists them.
const (
	// DeviceKey is the device's private key.
	DeviceKey = "device.key"
	// DeviceToken is the device token the server gave at login.
	DeviceToken = "credentials"
)

// MaxValueSize is the largest value a Store keeps, in bytes. The Windows Credential Manager takes
// at most 2560 bytes per entry; values are base64-encoded on the way to the system keychain, so
// this leaves room. The two entries are far smaller.
const MaxValueSize = 1024

var (
	// ErrNotFound: there is no such entry. It is the only error that means absent.
	ErrNotFound = errors.New("keyring: no such entry")
	// ErrUnavailable: the system keychain cannot be reached, and the entries are kept there, or
	// the store was asked to use it. It means unknown, not absent.
	ErrUnavailable = errors.New("keyring: the system keychain is not available")
	// ErrInsecure: a fallback file can be read by others than its owner, or is not a regular
	// file. It is not used until its permissions are fixed.
	ErrInsecure = errors.New("keyring: the file is not private")
	// ErrTimeout: the system keychain did not answer in time.
	ErrTimeout = errors.New("keyring: the system keychain did not answer in time")
	// ErrTooBig: the value is larger than MaxValueSize.
	ErrTooBig = errors.New("keyring: the value is too large")
	// ErrInvalidName: the name is not one a Store accepts.
	ErrInvalidName = errors.New("keyring: invalid entry name")
)

// Store keeps small secrets by name.
type Store interface {
	// Get returns the value of the entry. It returns an error matching ErrNotFound when there is
	// no such entry; any other error means it is not known whether there is one.
	Get(name string) ([]byte, error)
	// Set stores the value, replacing the entry if there is one.
	Set(name string, value []byte) error
	// Delete removes the entry. Removing an entry that does not exist is not an error.
	Delete(name string) error
	// Kind says where the entries are kept; Unknown when that cannot be told right now.
	Kind() Kind
}

// Kind is where a Store keeps its entries.
type Kind int

const (
	// Unknown: it cannot be told right now, because the system keychain did not answer.
	Unknown Kind = iota
	// System: the system keychain.
	System
	// Protected: a file encrypted for the current user by the system (DPAPI on Windows).
	Protected
	// PlainFile: a file only its owner can read, not encrypted.
	PlainFile
)

// String returns the name of the kind: "unknown", "system", "protected-file" or "plain-file".
func (k Kind) String() string {
	switch k {
	case System:
		return "system"
	case Protected:
		return "protected-file"
	case PlainFile:
		return "plain-file"
	}
	return "unknown"
}

// IsFile reports whether the entries are kept in files.
func (k Kind) IsFile() bool { return k == Protected || k == PlainFile }

// names are the entries a Store keeps. The list is closed on purpose: the keychain holds these two
// small things and nothing else, which keeps the number of entries fixed and lets the store tell
// when it holds nothing at all. Keeping something new here is a decision to make in this list.
var names = []string{DeviceKey, DeviceToken}

func checkName(name string) error {
	for _, n := range names {
		if name == n {
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrInvalidName, name)
}

func checkValue(value []byte) error {
	if len(value) > MaxValueSize {
		return fmt.Errorf("%w: %d bytes, at most %d", ErrTooBig, len(value), MaxValueSize)
	}
	return nil
}
