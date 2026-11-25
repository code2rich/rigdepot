// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package masterkey

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Rotate makes a new master key current and keeps every key of k as a
// retired one, so that whatever is still sealed under them keeps opening
// (records not rewrapped yet, an older backup). It returns the new keyring.
//
// A keyring from a file is rewritten in place (atomically); out, when set,
// writes the new file there instead. A key given directly in the
// environment cannot be rewritten, so out is required then, and the server
// has to be pointed at that file afterwards.
//
// Rotate only changes the key file. Rewrapping the records is RewrapAll's,
// and it is safe to run it after Rotate in a separate step: until it has
// run, the records open with the retired key.
func (k *Keyring) Rotate(out string) (*Keyring, error) {
	secret := make([]byte, KeySize)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	fresh, err := newKey(secret)
	if err != nil {
		return nil, err
	}
	k.mu.RLock()
	keys := append([]key{fresh}, k.keys...)
	source, path := k.source, k.path
	k.mu.RUnlock()

	switch {
	case out != "" && (path == "" || filepath.Clean(out) != filepath.Clean(path)):
		if err := writeNewFile(out, keys); err != nil {
			return nil, err
		}
		path = out
	case source == FromEnv || path == "":
		return nil, errors.New("masterkey: the master key was given in the environment; name a new key file to write the rotated keys to")
	default:
		if err := replaceFile(path, keys); err != nil {
			return nil, err
		}
	}
	return &Keyring{keys: keys, source: FromFile, path: path}, nil
}

// Rewrapper rewraps the sealed records of one table with the current key of
// k, inside tx, and returns how many it changed. Every domain package that
// stores sealed values provides one; "keys rotate" runs them all in one
// transaction.
type Rewrapper struct {
	Name   string // what it rewraps, for the summary, such as "settings"
	Rewrap func(ctx context.Context, tx *dbx.Tx, k *Keyring) (int, error)
}

// RewrapAll runs every rewrapper in one transaction: either every record is
// moved to the current key or none is. It returns the counts by name.
func RewrapAll(ctx context.Context, db *dbx.DB, k *Keyring, rewrappers []Rewrapper) (map[string]int, error) {
	counts := map[string]int{}
	err := db.InTx(ctx, func(tx *dbx.Tx) error {
		for _, r := range rewrappers {
			n, err := r.Rewrap(ctx, tx, k)
			if err != nil {
				return fmt.Errorf("rewrap %s: %w", r.Name, err)
			}
			counts[r.Name] = n
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return counts, nil
}
