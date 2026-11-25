// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package settings

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
)

// Authorizer decides whether actor may do action, such as
// "settings.update". An error means it could not decide, which refuses.
type Authorizer func(ctx context.Context, actor audit.Actor, action string) (bool, error)

// Options are what the server passes in.
type Options struct {
	// Authorize asks the access package. Nil refuses every protected
	// operation.
	Authorize Authorizer
	// SetupToken is the setup token given in the configuration
	// (RGD_SETUP_TOKEN); "" generates one on first start.
	SetupToken string
}

// Service is the settings of one database.
type Service struct {
	db   *dbx.DB
	keys *masterkey.Keyring
	log  *audit.Log
	opts Options
	now  func() time.Time

	failMu   sync.Mutex
	failures []time.Time // recent wrong setup tokens
}

// New returns the settings on db, sealing with keys and auditing into log.
func New(db *dbx.DB, keys *masterkey.Keyring, log *audit.Log, opts Options) *Service {
	return &Service{db: db, keys: keys, log: log, opts: opts, now: time.Now}
}

// SetClock replaces the clock. Tests use it.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// The errors of this package. The HTTP layer maps them to problem codes.
var (
	ErrDenied             = errors.New("settings: not allowed")
	ErrPreconditionFailed = errors.New("settings: changed since it was read")
	ErrSetupClosed        = errors.New("settings: the setup wizard is complete")
	ErrSetupToken         = errors.New("settings: the setup token is not valid")
	ErrSetupRateLimited   = errors.New("settings: too many wrong setup tokens")
	ErrAdminExists        = errors.New("settings: the first administrator already exists")
)

// ValidationError says which field of a change is not acceptable.
type ValidationError struct {
	Field string // such as "mail.host"
	Code  string // such as "required", "invalid"
}

func (e *ValidationError) Error() string { return fmt.Sprintf("settings: %s is %s", e.Field, e.Code) }

// The keys of the settings table.
const (
	keyOrgName      = "organization.name"
	keyMail         = "mail"
	keyMailPassword = "mail.password" // sealed
	keyModules      = "modules.enabled"
	keyAuditKey     = "audit.key"   // sealed; never shown
	keySetupToken   = "setup.token" // sealed; removed when the wizard completes
	keyAdminCreated = "setup.admin_created_at"
	keySetupDone    = "setup.completed_at"
)

// sealAD binds a sealed setting to its key: a sealed value copied to
// another key does not open.
func sealAD(key string) []byte { return []byte("settings/" + key) }

// row reads one setting. found is false when it is not there.
func row(ctx context.Context, q dbx.Querier, key string) (value string, encrypted, found bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT value, encrypted FROM settings WHERE key = ?`, key).Scan(&value, &encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, fmt.Errorf("settings: read %s: %w", key, err)
	}
	return value, encrypted, true, nil
}

// getJSON reads a plain setting into v.
func getJSON(ctx context.Context, q dbx.Querier, key string, v any) (bool, error) {
	value, encrypted, found, err := row(ctx, q, key)
	if err != nil || !found {
		return false, err
	}
	if encrypted {
		return false, fmt.Errorf("settings: %s is sealed", key)
	}
	if err := json.Unmarshal([]byte(value), v); err != nil {
		// Not "unset": a value that does not parse is unknown.
		return false, fmt.Errorf("settings: %s does not parse: %w", key, err)
	}
	return true, nil
}

// getSealed reads and opens a sealed setting.
func (s *Service) getSealed(ctx context.Context, q dbx.Querier, key string) ([]byte, bool, error) {
	return getSealed(ctx, q, s.keys, key)
}

func getSealed(ctx context.Context, q dbx.Querier, keys *masterkey.Keyring, key string) ([]byte, bool, error) {
	value, encrypted, found, err := row(ctx, q, key)
	if err != nil || !found {
		return nil, false, err
	}
	if !encrypted {
		return nil, false, fmt.Errorf("settings: %s is not sealed", key)
	}
	sealed, err := masterkey.Decode(value)
	if err != nil {
		return nil, false, fmt.Errorf("settings: %s: %w", key, err)
	}
	plain, err := keys.Open(sealed, sealAD(key))
	if err != nil {
		return nil, false, fmt.Errorf("settings: open %s: %w", key, err)
	}
	return plain, true, nil
}

// byWhom is the updated_by column: NULL for the system.
func byWhom(a audit.Actor) any {
	if a.UserID == "" {
		return nil
	}
	return a.UserID
}

func put(ctx context.Context, tx *dbx.Tx, key, value string, encrypted bool, by audit.Actor, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, encrypted, updated_by, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, encrypted = excluded.encrypted,
		updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		key, value, encrypted, byWhom(by), dbx.Millis(now))
	if err != nil {
		return fmt.Errorf("settings: write %s: %w", key, err)
	}
	return nil
}

func putJSON(ctx context.Context, tx *dbx.Tx, key string, v any, by audit.Actor, now time.Time) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return put(ctx, tx, key, string(b), false, by, now)
}

func (s *Service) putSealed(ctx context.Context, tx *dbx.Tx, key string, plain []byte, by audit.Actor) error {
	sealed, err := s.keys.Seal(plain, sealAD(key))
	if err != nil {
		return err
	}
	return put(ctx, tx, key, sealed.Encode(), true, by, s.now())
}

func remove(ctx context.Context, tx *dbx.Tx, key string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("settings: remove %s: %w", key, err)
	}
	return nil
}

// authorize asks the Authorizer and records a refusal in the audit log.
func (s *Service) authorize(ctx context.Context, actor audit.Actor, action string, object audit.Object) error {
	ok := false
	var err error
	if s.opts.Authorize != nil {
		ok, err = s.opts.Authorize(ctx, actor, action)
	}
	if err != nil {
		return fmt.Errorf("settings: check permission for %s: %w", action, err)
	}
	if ok {
		return nil
	}
	reason := "not allowed"
	if s.opts.Authorize == nil {
		reason = "no permission checks are configured"
	}
	if _, err := s.log.Record(ctx, audit.Entry{Actor: actor, Action: action, Object: object, Result: audit.ResultDenied, Reason: reason}); err != nil {
		return err
	}
	return ErrDenied
}

// lockKey serializes changes to the settings across instances.
const lockKey = "settings"

// AuditKey returns the audit log's chain key, sealed in the settings. With
// create, a database that has none gets a new random one; without, it
// returns nil when there is none.
//
// It is also the check that the master key is the one the database was
// sealed with: a wrong key fails here, at start, rather than later on
// someone's request.
func AuditKey(ctx context.Context, db *dbx.DB, keys *masterkey.Keyring, create bool) ([]byte, error) {
	key, found, err := getSealed(ctx, db, keys, keyAuditKey)
	if err != nil {
		if errors.Is(err, masterkey.ErrUnknownKey) || errors.Is(err, masterkey.ErrOpen) {
			return nil, fmt.Errorf("the master key does not open this database's secrets; is it the key this database was set up with? (%w)", err)
		}
		return nil, err
	}
	if found || !create {
		return key, nil
	}
	err = db.InTx(ctx, func(tx *dbx.Tx) error {
		if err := tx.Lock(ctx, lockKey); err != nil {
			return err
		}
		// Another instance may have created it meanwhile.
		if _, _, found, err := row(ctx, tx, keyAuditKey); err != nil || found {
			return err
		}
		fresh := make([]byte, audit.KeySize)
		if _, err := rand.Read(fresh); err != nil {
			return err
		}
		sealed, err := keys.Seal(fresh, sealAD(keyAuditKey))
		if err != nil {
			return err
		}
		return put(ctx, tx, keyAuditKey, sealed.Encode(), true, audit.System, time.Now())
	})
	if err != nil {
		return nil, err
	}
	key, _, err = getSealed(ctx, db, keys, keyAuditKey)
	return key, err
}

// HasSealedData reports whether the database holds anything sealed with a
// master key. The server generates a new master key only when it does not:
// a missing key file next to a database that needs it is an error to
// report, not a reason to start over.
func HasSealedData(ctx context.Context, db *dbx.DB) (bool, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE encrypted = TRUE`).Scan(&n); err != nil {
		return false, fmt.Errorf("settings: %w", err)
	}
	return n > 0, nil
}

// Rewrapper moves every sealed setting to the current master key, for
// "keys rotate".
var Rewrapper = masterkey.Rewrapper{Name: "settings", Rewrap: rewrap}

func rewrap(ctx context.Context, tx *dbx.Tx, keys *masterkey.Keyring) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key, value FROM settings WHERE encrypted = TRUE ORDER BY key`)
	if err != nil {
		return 0, err
	}
	type sealedRow struct{ key, value string }
	var all []sealedRow
	for rows.Next() {
		var r sealedRow
		if err := rows.Scan(&r.key, &r.value); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range all {
		sealed, err := masterkey.Decode(r.value)
		if err != nil {
			return n, fmt.Errorf("%s: %w", r.key, err)
		}
		sealed, changed, err := keys.Rewrap(sealed)
		if err != nil {
			return n, fmt.Errorf("%s: %w", r.key, err)
		}
		if !changed {
			continue
		}
		// updated_at and updated_by stay: nobody changed the setting.
		if _, err := tx.ExecContext(ctx, `UPDATE settings SET value = ? WHERE key = ?`, sealed.Encode(), r.key); err != nil {
			return n, fmt.Errorf("%s: %w", r.key, err)
		}
		n++
	}
	return n, nil
}
