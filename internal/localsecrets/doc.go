// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package localsecrets keeps the local, still-encrypted copies of personal secrets and unseals
// them at the moment of use.
//
// The copies are in secrets/vault.json in the local data directory, exactly as the server sent
// them (POST /api/v1/sync/secrets): each value sealed with HPKE to this computer's device key
// (package device). Nothing in the file opens without the device's private key, which is in the
// system keychain, so the file holds no secret in the clear and is not itself in the keychain
// (docs: 04a-platforms.md, "密钥在三个平台怎么存").
//
// Open unseals values for the command that needs them (rgd mcp run, serve, call) and keeps them
// in memory only. Before it opens a value it checks the HPKE info the server sealed it with: it
// must name this person, this device, the value's name and version, so that a copy made for
// another computer, person or name is refused rather than used.
//
// Failures say what they mean. A file that cannot be read or parsed is ErrUnknown, never an
// empty vault: nothing is replaced or deleted then. A missing copy is a *MissingError that names
// the secrets, so that the person is told which to set and where. A device key that cannot be
// read is the caller's to report (device.StoreError): this package never makes a key pair.
package localsecrets
