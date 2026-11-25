// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package secretbackend is the secret backend extension point (E4). Version: v1.
//
// The server keeps every personal secret (and, later, every password of an environment
// resource) with envelope encryption: the value is encrypted with a data key of its own, and
// the data key is wrapped by a key the server's database does not hold. A Backend is that
// second step. The server's built-in one wraps data keys with a key derived from the master
// key (a file or an environment variable, docs: 06-server.md, "密钥加密"); an optional module
// can wrap them with HashiCorp Vault's transit engine or a cloud KMS instead, so that the key
// never reaches the server's memory.
//
// What stays the same whatever the backend: the value's own encryption (AES-256-GCM under the
// data key, with additional data that binds it to its owner, name and version), where records
// are kept (the server's database, in the same transaction as the audit entry), and who may
// read them (nobody through the API; the server only to re-seal a value for one device).
//
// A backend that fails refuses: the core neither stores nor hands out a value then, and never
// falls back to another backend.
package secretbackend
