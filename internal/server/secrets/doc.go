// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package secrets manages secret definitions and values and seals them per device for delivery.
//
// A definition is a name a resource refers to with ${secret:NAME}, with a label and where to
// get one; a value is one person's token for it. Values are write-only: they come in through
// Set, from the person's browser or terminal, and go out only through SealFor, re-encrypted
// for one of the person's computers. No function returns a value as it is.
//
// # How a value is kept
//
// Envelope encryption (docs: 06-server.md, "密钥加密"). Set draws a data key for the record,
// encrypts the value with it (AES-256-GCM), and has the backend (ext/secretbackend, E4) wrap
// the data key; the built-in backend wraps it with the master key (package masterkey). The
// additional data of both steps is "secret_values/<user>/<name>/<version>", so a record moved
// to another person or name, or put back at an older version, does not open. The columns of
// secret_values hold the ciphertext, the nonce, the wrapped data key and the identifier of the
// key that wrapped it, the latter for "rgd-server keys rotate" (Rewrapper).
//
// # Sealing for a device
//
// SealFor opens the person's values and seals each with HPKE (RFC 9180; DHKEM(X25519,
// HKDF-SHA256), HKDF-SHA256, AES-256-GCM; crypto/hpke) to the public key the device sent when
// it signed in. The HPKE info binds user, device, name and version (Info), and the command line
// checks it before it opens a value. The plain value exists only in memory, for the moment
// between opening and sealing.
//
// # Definitions
//
// Define records the labels and instructions a resource repository gives for its secrets (the
// secrets of rigdepot.yaml). UseSecrets records which resource refers to which secret
// (resource_secrets) and defines a name nobody described yet with an empty label; the server
// calls it when a resource is published. Set does the same for the name it stores.
//
// The audit log records that a secret was replaced or deleted, never the value.
package secrets
