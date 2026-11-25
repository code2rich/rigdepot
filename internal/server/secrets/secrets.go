// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/ext/secretbackend"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
)

// MaxValue is the longest value Set takes, in bytes (the contract's SetSecretRequest).
const MaxValue = 8192

// Suite names the HPKE cipher suite of SealFor, as the contract writes it.
const Suite = "hpke-x25519-sha256-aes256gcm"

// The audit actions of this package. Setting or deleting one's own secret is not in the access
// table: only the person themself may, never for someone else.
const (
	ActionSet    = "secret.set"
	ActionDelete = "secret.delete"
)

var (
	// ErrDenied: the caller may not set or read secrets this way (nobody signed in, or an access
	// token: secrets are entered only in the browser or in one's own terminal).
	ErrDenied = errors.New("secrets: not allowed")
	// ErrNotFound: the person has no value for the name.
	ErrNotFound = errors.New("secrets: no such secret")
	// ErrInvalidName: not a secret name (resource.ValidSecretName).
	ErrInvalidName = errors.New("secrets: not a secret name")
	// ErrInvalidValue: empty, longer than MaxValue, or not text.
	ErrInvalidValue = errors.New("secrets: the value is empty, too long or not text")
	// ErrInvalidKey: the device's public key is not an X25519 key.
	ErrInvalidKey = errors.New("secrets: the device's public key is not an X25519 key")
)

// Options are what a Service needs besides the database and the master key.
type Options struct {
	Audit *audit.Log
	// Backend wraps the data keys. Nil is the built-in one, with the master key.
	Backend secretbackend.Backend
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Service keeps personal secrets.
type Service struct {
	db      *dbx.DB
	audit   *audit.Log
	backend secretbackend.Backend
	now     func() time.Time
}

// New returns the service on an open, migrated database.
func New(db *dbx.DB, keys *masterkey.Keyring, o Options) *Service {
	s := &Service{db: db, audit: o.Audit, backend: o.Backend, now: o.Now}
	if s.backend == nil {
		s.backend = Builtin(keys)
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// Def is the definition of a secret.
type Def struct {
	Name  string
	Label string // what it is, for people; "" when nobody described it
	Howto string // where to get one
}

// Value is what may be said about a person's value: never the value.
type Value struct {
	Name string
	// Version counts the replacements; it is bound into the encryption.
	Version   int
	UpdatedAt time.Time
	ExpiresAt time.Time // zero when unknown
}

// Sealed is one value sealed for a device.
type Sealed struct {
	Name    string
	Version int
	// Enc is the HPKE encapsulated key and Ciphertext the sealed value.
	Enc, Ciphertext []byte
	// Info is the HPKE info it was sealed with (Info).
	Info      string
	ExpiresAt time.Time
}

// Device is the computer SealFor seals for.
type Device struct {
	ID string
	// PublicKey is its X25519 public key, 32 bytes in unpadded base64url, as it signed in.
	PublicKey string
}

// Info is the HPKE info of a value sealed for a device: it names the scheme and binds user,
// device, name and version, so that a sealed value copied to another computer, person, name or
// version does not open there.
func Info(userID, deviceID, name string, version int) string {
	return strings.Join([]string{brand.CLI + "/secret/v1", userID, deviceID, name, strconv.Itoa(version)}, "|")
}

// valueAD is the additional data of a stored value and of its wrapped data key.
func valueAD(userID, name string, version int) []byte {
	return []byte("secret_values/" + userID + "/" + name + "/" + strconv.Itoa(version))
}

// caller checks that a is a person in the browser or in their own terminal.
func caller(a access.Actor) error {
	if a.UserID == "" || a.TokenID != "" || (a.Origin != access.OriginWeb && a.Origin != access.OriginCLI) {
		return ErrDenied
	}
	return nil
}

func auditActor(a access.Actor) audit.Actor {
	return audit.Actor{UserID: a.UserID, Origin: audit.Origin(a.Origin), ViaTool: a.ViaTool, DeviceID: a.DeviceID}
}

func secretObject(name string) audit.Object {
	return audit.Object{Kind: "secret", ID: name, Label: name}
}

// Define records definitions, replacing the label and instructions of names that have them.
func (s *Service) Define(ctx context.Context, defs []Def) error {
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		now := dbx.Millis(s.now())
		for _, d := range defs {
			if !resource.ValidSecretName(d.Name) {
				return fmt.Errorf("%w: %q", ErrInvalidName, d.Name)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO secret_defs (name, display_name, help_url, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (name) DO UPDATE SET display_name = excluded.display_name, help_url = excluded.help_url, updated_at = excluded.updated_at`,
				d.Name, d.Label, d.Howto, now, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// ensureDefs defines the names that are not defined yet, with empty labels.
func (s *Service) ensureDefs(ctx context.Context, tx *dbx.Tx, names []string) error {
	now := dbx.Millis(s.now())
	for _, n := range names {
		if _, err := tx.ExecContext(ctx, `INSERT INTO secret_defs (name, display_name, help_url, created_at, updated_at) VALUES (?, '', '', ?, ?)
			ON CONFLICT (name) DO NOTHING`, n, now, now); err != nil {
			return err
		}
	}
	return nil
}

// UseSecrets records that the resource refers to exactly these secrets, defining the names
// nobody defined yet.
func (s *Service) UseSecrets(ctx context.Context, resourceID string, names []string) error {
	for _, n := range names {
		if !resource.ValidSecretName(n) {
			return fmt.Errorf("%w: %q", ErrInvalidName, n)
		}
	}
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if err := s.ensureDefs(ctx, tx, names); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM resource_secrets WHERE resource_id = ?`, resourceID); err != nil {
			return err
		}
		for _, n := range slices.Compact(slices.Sorted(slices.Values(names))) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO resource_secrets (resource_id, secret_name) VALUES (?, ?)`, resourceID, n); err != nil {
				return err
			}
		}
		return nil
	})
}

// Defs returns the definitions of the names that have one.
func (s *Service) Defs(ctx context.Context, names []string) (map[string]Def, error) {
	out := map[string]Def{}
	if len(names) == 0 {
		return out, nil
	}
	args := make([]any, len(names))
	for i, n := range names {
		args[i] = n
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name, display_name, help_url FROM secret_defs WHERE name IN (`+placeholders(len(names))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d Def
		if err := rows.Scan(&d.Name, &d.Label, &d.Howto); err != nil {
			return nil, err
		}
		out[d.Name] = d
	}
	return out, rows.Err()
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// Values returns, by name, what may be said about the values the person set.
func (s *Service) Values(ctx context.Context, userID string) (map[string]Value, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT secret_name, version, updated_at, expires_at FROM secret_values WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Value{}
	for rows.Next() {
		var v Value
		var updated int64
		var expires sql.NullInt64
		if err := rows.Scan(&v.Name, &v.Version, &updated, &expires); err != nil {
			return nil, err
		}
		v.UpdatedAt = dbx.Time(updated)
		if expires.Valid {
			v.ExpiresAt = dbx.Time(expires.Int64)
		}
		out[v.Name] = v
	}
	return out, rows.Err()
}

// Configured returns, by name, which secrets the person has set.
func (s *Service) Configured(ctx context.Context, userID string) (map[string]bool, error) {
	vs, err := s.Values(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(vs))
	for n := range vs {
		out[n] = true
	}
	return out, nil
}

// Set stores the caller's value for name, replacing the one before. The value is encrypted at
// once; expires is when it stops working, zero when unknown.
func (s *Service) Set(ctx context.Context, a access.Actor, name string, value []byte, expires time.Time) (Value, error) {
	if err := caller(a); err != nil {
		return Value{}, err
	}
	if !resource.ValidSecretName(name) {
		return Value{}, ErrInvalidName
	}
	if len(value) == 0 || len(value) > MaxValue || !validText(value) {
		return Value{}, ErrInvalidValue
	}
	var out Value
	err := s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if err := s.ensureDefs(ctx, tx, []string{name}); err != nil {
			return err
		}
		version := 1
		var prev int
		err := tx.QueryRowContext(ctx, `SELECT version FROM secret_values WHERE user_id = ? AND secret_name = ?`, a.UserID, name).Scan(&prev)
		switch {
		case err == nil:
			version = prev + 1
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		rec, err := s.seal(ctx, value, valueAD(a.UserID, name, version))
		if err != nil {
			return err
		}
		now := s.now()
		var exp sql.NullInt64
		if !expires.IsZero() {
			exp = sql.NullInt64{Int64: dbx.Millis(expires), Valid: true}
		}
		enc := base64.StdEncoding.EncodeToString
		args := []any{enc(rec.ciphertext), enc(rec.nonce), enc(rec.wrapped.Data), rec.wrapped.KeyID, version, exp, dbx.Millis(now), a.UserID, name}
		if version == 1 {
			_, err = tx.ExecContext(ctx, `INSERT INTO secret_values (ciphertext, nonce, wrapped_key, key_id, version, expires_at, updated_at, user_id, secret_name)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE secret_values SET ciphertext = ?, nonce = ?, wrapped_key = ?, key_id = ?, version = ?, expires_at = ?, updated_at = ?
				WHERE user_id = ? AND secret_name = ?`, args...)
		}
		if err != nil {
			return err
		}
		out = Value{Name: name, Version: version, UpdatedAt: dbx.Time(dbx.Millis(now))}
		if exp.Valid {
			out.ExpiresAt = dbx.Time(exp.Int64)
		}
		_, err = s.audit.Append(ctx, tx, audit.Entry{
			Actor: auditActor(a), Action: ActionSet, Object: secretObject(name),
			Change: map[string]any{"value": "replaced", "version": version},
		})
		return err
	})
	return out, err
}

// validText reports whether a value is text without line breaks or other control characters: a
// token, as every tool passes it on in an environment variable or a header.
func validText(v []byte) bool {
	if !utf8.Valid(v) {
		return false
	}
	for _, r := range string(v) {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// Delete deletes the caller's value for name. Each computer drops its copy at its next sync.
func (s *Service) Delete(ctx context.Context, a access.Actor, name string) error {
	if err := caller(a); err != nil {
		return err
	}
	if !resource.ValidSecretName(name) {
		return ErrNotFound
	}
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM secret_values WHERE user_id = ? AND secret_name = ?`, a.UserID, name)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		_, err = s.audit.Append(ctx, tx, audit.Entry{Actor: auditActor(a), Action: ActionDelete, Object: secretObject(name)})
		return err
	})
}

// SealFor seals the values of the person a for one of their devices: the names asked for, or
// every value when names is empty. It also returns the names asked for that have no value.
//
// The caller has made sure that d is a live device of the person, with the public key it signed
// in with.
func (s *Service) SealFor(ctx context.Context, a access.Actor, d Device, names []string) ([]Sealed, []string, error) {
	if a.UserID == "" || a.DeviceID == "" || a.DeviceID != d.ID {
		return nil, nil, ErrDenied
	}
	raw, err := base64.RawURLEncoding.DecodeString(d.PublicKey)
	if err != nil {
		return nil, nil, ErrInvalidKey
	}
	pub, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		return nil, nil, ErrInvalidKey
	}
	pk, err := hpke.NewDHKEMPublicKey(pub)
	if err != nil {
		return nil, nil, ErrInvalidKey
	}
	rows, err := s.db.QueryContext(ctx, `SELECT secret_name, ciphertext, nonce, wrapped_key, key_id, version, expires_at FROM secret_values
		WHERE user_id = ? ORDER BY secret_name`, a.UserID)
	if err != nil {
		return nil, nil, err
	}
	var recs []storedRow
	for rows.Next() {
		var r storedRow
		if err := rows.Scan(&r.name, &r.ciphertext, &r.nonce, &r.wrapped, &r.keyID, &r.version, &r.expires); err != nil {
			rows.Close()
			return nil, nil, err
		}
		recs = append(recs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []Sealed
	have := map[string]bool{}
	for _, r := range recs {
		if len(want) > 0 && !want[r.name] {
			continue
		}
		have[r.name] = true
		value, err := s.open(ctx, r, valueAD(a.UserID, r.name, r.version))
		if err != nil {
			return nil, nil, fmt.Errorf("secrets: %s: %w", r.name, err)
		}
		info := Info(a.UserID, d.ID, r.name, r.version)
		enc, sender, err := hpke.NewSender(pk, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(info))
		if err != nil {
			clear(value)
			return nil, nil, err
		}
		ct, err := sender.Seal(nil, value)
		clear(value)
		if err != nil {
			return nil, nil, err
		}
		sv := Sealed{Name: r.name, Version: r.version, Enc: enc, Ciphertext: ct, Info: info}
		if r.expires.Valid {
			sv.ExpiresAt = dbx.Time(r.expires.Int64)
		}
		out = append(out, sv)
	}
	var missing []string
	for _, n := range names {
		if !have[n] && !slices.Contains(missing, n) {
			missing = append(missing, n)
		}
	}
	slices.Sort(missing)
	return out, missing, nil
}

// storedRow is a row of secret_values as stored.
type storedRow struct {
	name                       string
	ciphertext, nonce, wrapped string
	keyID                      string
	version                    int
	expires                    sql.NullInt64
}

// record is a value encrypted under its data key, with the data key wrapped.
type record struct {
	ciphertext, nonce []byte
	wrapped           secretbackend.Wrapped
}

// seal encrypts value under a fresh data key and has the backend wrap the data key.
func (s *Service) seal(ctx context.Context, value, ad []byte) (record, error) {
	dek := make([]byte, secretbackend.DataKeySize)
	if _, err := rand.Read(dek); err != nil {
		return record{}, err
	}
	defer clear(dek)
	gcm, err := newGCM(dek)
	if err != nil {
		return record{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return record{}, err
	}
	w, err := s.backend.Wrap(ctx, dek, ad)
	if err != nil {
		return record{}, fmt.Errorf("secrets: the %s backend: %w", s.backend.ID(), err)
	}
	return record{ciphertext: gcm.Seal(nil, nonce, value, ad), nonce: nonce, wrapped: w}, nil
}

// errOpen: a stored value does not open: it was changed, or moved.
var errOpen = errors.New("the stored value does not open")

// open decrypts a stored value.
func (s *Service) open(ctx context.Context, r storedRow, ad []byte) ([]byte, error) {
	dec := base64.StdEncoding.DecodeString
	ct, err1 := dec(r.ciphertext)
	nonce, err2 := dec(r.nonce)
	wrapped, err3 := dec(r.wrapped)
	if err := errors.Join(err1, err2, err3); err != nil {
		return nil, errOpen
	}
	dek, err := s.backend.Unwrap(ctx, secretbackend.Wrapped{KeyID: r.keyID, Data: wrapped}, ad)
	if err != nil {
		return nil, fmt.Errorf("the %s backend: %w", s.backend.ID(), err)
	}
	defer clear(dek)
	gcm, err := newGCM(dek)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return nil, errOpen
	}
	v, err := gcm.Open(nil, nonce, ct, ad)
	if err != nil {
		return nil, errOpen
	}
	return v, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
