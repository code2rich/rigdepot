// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package secrets

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/code2rich/rigdepot/ext/secretbackend"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
)

// BuiltinID is the ID of the built-in backend.
const BuiltinID = "builtin"

// Builtin returns the built-in secret backend: it wraps data keys with the master key
// (masterkey.Keyring.Seal, itself envelope encryption under a key derived from the master key).
// Wrapped.KeyID is the master key's identifier, which "keys rotate" uses.
func Builtin(keys *masterkey.Keyring) secretbackend.Backend { return builtin{keys: keys} }

type builtin struct{ keys *masterkey.Keyring }

var (
	_ secretbackend.Backend   = builtin{}
	_ secretbackend.Rewrapper = builtin{}
)

func (builtin) ID() string { return BuiltinID }

func (b builtin) Wrap(_ context.Context, dataKey, ad []byte) (secretbackend.Wrapped, error) {
	s, err := b.keys.Seal(dataKey, ad)
	if err != nil {
		return secretbackend.Wrapped{}, err
	}
	return secretbackend.Wrapped{KeyID: s.KeyID, Data: []byte(s.Encode())}, nil
}

func (b builtin) Unwrap(_ context.Context, w secretbackend.Wrapped, ad []byte) ([]byte, error) {
	s, err := masterkey.Decode(string(w.Data))
	if err != nil {
		return nil, err
	}
	if s.KeyID != w.KeyID {
		return nil, masterkey.ErrOpen
	}
	return b.keys.Open(s, ad)
}

func (b builtin) Rewrap(_ context.Context, w secretbackend.Wrapped, _ []byte) (secretbackend.Wrapped, bool, error) {
	s, err := masterkey.Decode(string(w.Data))
	if err != nil {
		return w, false, err
	}
	s, changed, err := b.keys.Rewrap(s)
	if err != nil || !changed {
		return w, false, err
	}
	return secretbackend.Wrapped{KeyID: s.KeyID, Data: []byte(s.Encode())}, true, nil
}

// Rewrapper moves the data key of every stored value to the current master key, for
// "rgd-server keys rotate". It applies to values the built-in backend wrapped; with another
// backend configured, rotating is that backend's.
var Rewrapper = masterkey.Rewrapper{Name: "secrets", Rewrap: rewrap}

func rewrap(ctx context.Context, tx *dbx.Tx, keys *masterkey.Keyring) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT user_id, secret_name, wrapped_key, key_id FROM secret_values WHERE key_id <> ? ORDER BY user_id, secret_name`,
		keys.CurrentID())
	if err != nil {
		return 0, err
	}
	type row struct{ user, name, wrapped, keyID string }
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.user, &r.name, &r.wrapped, &r.keyID); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	b := builtin{keys: keys}
	n := 0
	for _, r := range all {
		data, err := base64.StdEncoding.DecodeString(r.wrapped)
		if err != nil {
			return n, fmt.Errorf("%s of %s: %w", r.name, r.user, errOpen)
		}
		w, changed, err := b.Rewrap(ctx, secretbackend.Wrapped{KeyID: r.keyID, Data: data}, nil)
		if err != nil {
			return n, fmt.Errorf("%s of %s: %w", r.name, r.user, err)
		}
		if !changed {
			continue
		}
		res, err := tx.ExecContext(ctx, `UPDATE secret_values SET wrapped_key = ?, key_id = ? WHERE user_id = ? AND secret_name = ? AND key_id = ?`,
			base64.StdEncoding.EncodeToString(w.Data), w.KeyID, r.user, r.name, r.keyID)
		if err != nil {
			return n, err
		}
		if k, err := res.RowsAffected(); err != nil {
			return n, err
		} else if k != 1 {
			return n, errors.New("a stored secret changed while it was rewrapped")
		}
		n++
	}
	return n, nil
}
