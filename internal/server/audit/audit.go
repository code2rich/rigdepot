// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Origin is where an operation came from.
type Origin string

// The origins.
const (
	OriginWeb    Origin = "web"
	OriginCLI    Origin = "cli"
	OriginSystem Origin = "system"
)

// Category groups entries. Optional modules add their own.
type Category string

// The core's categories.
const (
	CategoryPlatform Category = "platform"
	CategoryLogin    Category = "login"
)

// Result is how an operation ended.
type Result string

// The results.
const (
	ResultOK      Result = "ok"
	ResultDenied  Result = "denied"
	ResultPending Result = "pending" // waiting for the person to confirm it on the web
)

// Actor is who did something, as the log records it: the person (even when
// an AI tool acted for them), where from, through which AI tool and on which
// computer. The access package's Actor converts to it.
type Actor struct {
	UserID   string // "" for the system itself, or a sign-in by nobody known
	Origin   Origin
	ViaTool  string // an adapter id such as "claude-code"; "" on the web
	DeviceID string
}

// System is the server acting on its own (a background job, first start).
var System = Actor{Origin: OriginSystem}

// Object is what an operation was about. Label is its name at the time, so
// that the entry stays readable after the object is renamed or removed.
type Object struct {
	Kind string // such as "resource", "user", "settings"
	ID   string // "" for something refused before it existed

	Label string
}

// Entry is one thing to record.
type Entry struct {
	Category Category // CategoryPlatform when empty
	Actor    Actor
	// Method is how someone signed in (local, oidc, device_code…), for
	// entries in CategoryLogin.
	Method string
	// Action is the stable identifier, such as "resource.publish": the same
	// table as the permission checks' actions.
	Action string
	Object Object
	// Change summarizes what changed: which fields, versions from and to,
	// how many rows. Never content and never secrets ("replaced" instead).
	// It is stored as a JSON object; nil is {}.
	Change map[string]any
	Result Result // ResultOK when empty
	Reason string // why it was denied or is pending
}

// Row is an entry as stored.
type Row struct {
	Seq      int64
	At       time.Time
	Category Category
	Actor    Actor
	Method   string
	Action   string
	Object   Object
	Change   string // the JSON object as stored
	Result   Result
	Reason   string
	PrevHash string
	Hash     string
}

// Log is the audit log of one database.
type Log struct {
	db  *dbx.DB
	key []byte
	now func() time.Time
}

// KeySize is the size of the chain's key.
const KeySize = 32

// New returns the log on db, chained with key. The key is kept sealed in the
// settings (settings.AuditKey); without it the chain cannot be extended or
// verified.
func New(db *dbx.DB, key []byte) (*Log, error) {
	if len(key) < KeySize {
		return nil, fmt.Errorf("audit: the chain key is %d bytes, want %d", len(key), KeySize)
	}
	return &Log{db: db, key: key, now: time.Now}, nil
}

// SetClock replaces the clock. Tests use it.
func (l *Log) SetClock(now func() time.Time) { l.now = now }

// ErrInvalid is wrapped by the errors Append returns for an entry that does
// not say what the log needs. It is a programming error in the caller.
var ErrInvalid = errors.New("audit: invalid entry")

var (
	actionPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	categoryPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

func (e *Entry) normalize() error {
	if e.Category == "" {
		e.Category = CategoryPlatform
	}
	if e.Result == "" {
		e.Result = ResultOK
	}
	switch {
	case !categoryPattern.MatchString(string(e.Category)):
		return fmt.Errorf("%w: category %q", ErrInvalid, e.Category)
	case !actionPattern.MatchString(e.Action):
		return fmt.Errorf("%w: action %q is not like resource.publish", ErrInvalid, e.Action)
	case e.Actor.Origin != OriginWeb && e.Actor.Origin != OriginCLI && e.Actor.Origin != OriginSystem:
		return fmt.Errorf("%w: origin %q", ErrInvalid, e.Actor.Origin)
	case e.Result != ResultOK && e.Result != ResultDenied && e.Result != ResultPending:
		return fmt.Errorf("%w: result %q", ErrInvalid, e.Result)
	case e.Object.Kind == "":
		// The id may be empty: a refused creation has no object yet.
		return fmt.Errorf("%w: %s has no object kind", ErrInvalid, e.Action)
	}
	return nil
}

// lockKey serializes appends across instances (dbx.Tx.Lock).
const lockKey = "audit_log"

// genesis is the previous hash of the first row.
const genesis = "0000000000000000000000000000000000000000000000000000000000000000"

// Append adds an entry inside tx, the transaction of the change it records,
// and returns its sequence number. Its error must fail the transaction: an
// operation is never committed without its entry.
func (l *Log) Append(ctx context.Context, tx *dbx.Tx, e Entry) (int64, error) {
	if err := e.normalize(); err != nil {
		return 0, err
	}
	change := []byte("{}")
	if len(e.Change) > 0 {
		var err error
		if change, err = json.Marshal(e.Change); err != nil {
			return 0, fmt.Errorf("%w: change: %v", ErrInvalid, err)
		}
	}
	if err := tx.Lock(ctx, lockKey); err != nil {
		return 0, err
	}
	var last int64
	prev := genesis
	err := tx.QueryRowContext(ctx, `SELECT seq, hash FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&last, &prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("audit: read the last entry: %w", err)
	}
	r := Row{
		Seq: last + 1, At: dbx.Time(dbx.Millis(l.now())), Category: e.Category, Actor: e.Actor,
		Method: e.Method, Action: e.Action, Object: e.Object, Change: string(change),
		Result: e.Result, Reason: e.Reason, PrevHash: prev,
	}
	r.Hash = l.hash(r)
	var actor any
	if r.Actor.UserID != "" {
		actor = r.Actor.UserID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_log
		(seq, at, category, actor_id, origin, via_tool, device_id, method, action,
		 object_kind, object_id, object_label, change, result, reason, prev_hash, hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Seq, dbx.Millis(r.At), string(r.Category), actor, string(r.Actor.Origin), r.Actor.ViaTool, r.Actor.DeviceID,
		r.Method, r.Action, r.Object.Kind, r.Object.ID, r.Object.Label, r.Change, string(r.Result), r.Reason,
		r.PrevHash, r.Hash)
	if err != nil {
		return 0, fmt.Errorf("audit: append %s: %w", r.Action, err)
	}
	return r.Seq, nil
}

// Record adds an entry in a transaction of its own. It is for operations
// that did not happen (refused ones) and so have no transaction to join.
func (l *Log) Record(ctx context.Context, e Entry) (int64, error) {
	var seq int64
	err := l.db.InTx(ctx, func(tx *dbx.Tx) error {
		var err error
		seq, err = l.Append(ctx, tx, e)
		return err
	})
	return seq, err
}
