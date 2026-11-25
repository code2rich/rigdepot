// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package secretbackend

import "context"

// DataKeySize is the size of a data key in bytes: an AES-256 key.
const DataKeySize = 32

// Backend wraps and unwraps data keys.
type Backend interface {
	// ID is lower-case letters, digits and single hyphens, such as "builtin" or "vault".
	ID() string

	// Wrap encrypts dataKey, DataKeySize random bytes, and returns what the core stores in
	// place of it. ad is additional data that binds the wrapped key to its record: Unwrap with
	// other additional data must fail, so that a record moved to another owner does not open.
	Wrap(ctx context.Context, dataKey, ad []byte) (Wrapped, error)

	// Unwrap returns the data key of w, given the ad it was wrapped with. A wrapped key that
	// does not open (changed bytes, other additional data, a key the backend no longer has) is
	// an error, and so is a backend that cannot be reached: the core refuses to read the value.
	Unwrap(ctx context.Context, w Wrapped, ad []byte) ([]byte, error)
}

// Wrapped is a wrapped data key as the core stores it.
type Wrapped struct {
	// KeyID names the backend's key that wrapped it, such as the master key's identifier or a
	// key version in Vault. The core keeps it in a column of its own, so that a rotation can
	// tell the records it still has to move.
	KeyID string
	// Data is the wrapped key, opaque to the core.
	Data []byte
}

// Rewrapper is implemented by a Backend whose keys can be rotated: after a rotation, the core
// calls Rewrap on every record so that the old key can be retired. The value itself is not
// touched and nobody has to enter it again.
type Rewrapper interface {
	// Rewrap wraps the data key of w with the backend's current key. It reports false, and
	// returns w as it is, when w already uses it.
	Rewrap(ctx context.Context, w Wrapped, ad []byte) (Wrapped, bool, error)
}
