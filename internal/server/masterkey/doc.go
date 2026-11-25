// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package masterkey loads and derives the master key and performs envelope encryption and key
// rotation.
//
// Everything secret the server stores (personal secrets, an identity provider's client secret, a
// repository's read-only token, the mail password, the audit log's key) is sealed the same way:
// a fresh data key per record encrypts the value with AES-256-GCM, and the data key is wrapped
// with AES-256-GCM under a key-encryption key derived from the master key with HKDF-SHA256. The
// caller's additional data binds the value to its owner, so a value moved to another record does
// not open. Rotating the master key rewraps the data keys and leaves the values alone. See
// docs/architecture/06-server.md#密钥加密.
//
// The master key comes, in this order, from the file RGD_MASTER_KEY_FILE names, from
// RGD_MASTER_KEY, or from a file generated in the data directory on first start (Load). A key
// file can hold several keys, one per line: the first encrypts, the others only decrypt what was
// sealed before a rotation.
package masterkey
