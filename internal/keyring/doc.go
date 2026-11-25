// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package keyring stores small secrets in the system keychain on macOS, Windows and Linux, with a
// file fallback.
//
// It keeps two things, both small: the device's private key (DeviceKey) and the device token
// (DeviceToken). The local copies of personal secrets do not go in here: they are ciphertext in
// the local data directory, sealed to the device's key (package localsecrets).
//
// # Where
//
// Open returns a Store that keeps its entries in the system keychain when there is one: the login
// keychain on macOS (through /usr/bin/security), the Credential Manager on Windows, the Secret
// Service on Linux (through D-Bus). When there is none (a server or a container without a desktop
// session, most WSL installations) it keeps them in files in the secrets directory of the local
// data directory instead: 0600 on macOS and Linux, encrypted with DPAPI for the current user on
// Windows. Kind tells which, and Options.OnFallback is told when a value first goes to a file, so
// that the command can say so.
//
// The choice is recorded in the secrets directory (MarkerFile) when the first value is stored,
// and kept from then on. A system keychain that held the values and is not available later is
// an error, not a reason to look in the files: the values are somewhere, just not readable now.
// Nothing moves values between the keychain and the files automatically.
//
// # Absent and unknown
//
// Get returns an error matching ErrNotFound only when the entry is known to be absent. Every other
// error, ErrUnavailable among them, means unknown: the keychain is locked, D-Bus did not answer, a
// file cannot be read or is readable by others. A caller that sees such an error stops. It does
// not delete the local copies of secrets and does not make a new device key pair, which would
// make every copy the server sealed to the old key useless.
//
// # Tests
//
// Tests never reach the real system keychain. NewMemory is a Store for the tests of other
// packages. Inside a test binary the system keychain behaves as if it were not there, so Open
// falls back to files in the temporary home, unless the test run sets EnvRealKeyring to 1; only
// the test of the real keychain in this package does, and it uses a service name of its own and
// removes what it wrote.
package keyring
