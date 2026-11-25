// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package device manages the device identity: key pair, device token and login state.
//
// A computer that signed in (rgd login) has two keychain entries (package keyring): the device's
// X25519 private key, which the server seals personal secrets to, and the credentials, which are
// the device token with the server that issued it and the account it belongs to. Login runs the
// device code flow (RFC 8628) against the server; Logout signs the device out there and deletes
// both entries.
//
// A keychain that cannot be read is never taken for an empty one: the key pair is only made when
// the keychain says there is none, and nothing is deleted while it cannot be read. A new key pair
// would make every secret the server sealed to the old one useless (docs: secrets, failures).
//
// The device token is sent only to the server that issued it (Identity.Connect,
// Identity.CredentialsFor).
package device
