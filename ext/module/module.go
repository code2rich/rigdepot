// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package module

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"time"
)

// Module is an optional module.
type Module interface {
	// ID is lower-case letters, digits and single hyphens, such as "insights". It is also the
	// route prefix (/api/v1/x/<id>/), the migration namespace, the prefix of its jobs, settings
	// and audit actions, and part of the build tag rgd_omit_<id> (with hyphens as underscores).
	// It never changes once released.
	ID() string
	// Describe says what the module is, for the settings page.
	Describe() Info
	// Start is called when the module is turned on, and on every start of the server while it is
	// on. Mount routes, run migrations, add jobs and subscribe to events here. ctx ends when the
	// module is turned off or the server stops; goroutines the module starts must end with it.
	// An error leaves the module off, and whatever it registered is taken back.
	Start(ctx context.Context, h Host) error
}

// Stopper is implemented by modules that must clean up when they are turned off, beyond what
// the host takes back by itself.
type Stopper interface {
	Stop(ctx context.Context) error
}

// Info describes a module. The texts are message keys, so that the settings page shows them in
// the reader's language: "module.<id>.name" and so on.
type Info struct {
	Name        string // message key of the display name
	Description string // message key of one sentence on what it does
	// Notice is the message key of what turning it on means (data it collects, what it sends
	// where), shown before an administrator turns it on. Empty when there is nothing to say.
	Notice string
	// External names the systems outside this server the module contacts once on, such as
	// "an OpenAI-compatible endpoint configured in its settings". Empty when it contacts none.
	External []string
}

// Host is what the core gives a running module.
type Host interface {
	// Routes mounts handlers under /api/v1/x/<id>/. Requests reach them authenticated.
	Routes() Router
	// Migrate applies the module's migrations (files named NNNN_name.sql at the top of fsys,
	// written like the core's) in the module's own namespace.
	Migrate(ctx context.Context, fsys fs.FS) error
	// DB is the database, with ? placeholders rewritten for the dialect.
	DB() DB
	// Jobs schedules background jobs.
	Jobs() Scheduler
	// Events subscribes to domain events.
	Events() Subscriber
	// Audit writes audit entries. Every change a module makes on someone's behalf is audited.
	Audit() AuditWriter
	// Authz says who is making a request and what they may do.
	Authz() Authorizer
	// Settings keeps the module's own settings.
	Settings() Settings
	// Logger logs with the module's ID attached.
	Logger() *slog.Logger
}

// Router mounts the module's handlers.
type Router interface {
	// Handle registers h for pattern, a net/http ServeMux pattern relative to /api/v1/x/<id>,
	// such as "GET /items/{id}". Like ServeMux.Handle, it panics on a malformed or conflicting
	// pattern; during Start that makes the start fail.
	Handle(pattern string, h http.Handler)
}

// Querier runs SQL: a DB or a Tx. Queries use ? placeholders and the portable subset of SQL the
// core's migrations use.
type Querier interface {
	Dialect() string // "sqlite" or "postgres"
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Tx is a write transaction.
type Tx interface {
	Querier
}

// DB is the database. A module reads and writes only the tables its own migrations created.
type DB interface {
	Querier
	// InTx runs fn in a write transaction, committed when fn returns nil and rolled back when it
	// returns an error.
	InTx(ctx context.Context, fn func(Tx) error) error
}

// Job is a background job of the module.
type Job struct {
	Name    string        // lower-case words separated by dashes; the host prefixes "<id>."
	Every   time.Duration // between the starts of two runs
	Timeout time.Duration // the run's context is canceled then
	Run     func(ctx context.Context) error
}

// Scheduler schedules the module's jobs. With several server instances, a job runs on one of
// them at a time.
type Scheduler interface {
	Add(j Job) error
	// Trigger asks for a run as soon as possible and reports whether the job exists.
	Trigger(name string) bool
}

// Event is a domain event, such as "resource.published", delivered after the change that caused
// it was committed.
type Event struct {
	ID      string
	Kind    string
	At      time.Time
	Payload json.RawMessage // identifiers and short labels, never content or secrets
}

// Subscriber subscribes to domain events.
type Subscriber interface {
	// Subscribe calls h for each event of the given kinds (every kind when empty), in order, on
	// a goroutine of its own. An error from h is logged; the event is not delivered again.
	Subscribe(kinds []string, h func(ctx context.Context, ev Event) error)
}

// Actor is who does something: the person, and through what.
type Actor struct {
	UserID   string
	Origin   string // "web", "cli" or "system"
	ViaTool  string // the AI tool, when the command line was driven by one
	DeviceID string
	TokenID  string // when an access token was used
}

// AuditEntry is one entry of the audit log. It never holds content or secrets: say what changed,
// not to what.
type AuditEntry struct {
	Action      string // "<module id>.<verb>", such as "insights.export"
	ObjectKind  string
	ObjectID    string
	ObjectLabel string         // the object's name at the time, readable after it is renamed
	Change      map[string]any // a summary of what changed
	Denied      bool           // the attempt was refused
	Reason      string         // why it was refused
}

// AuditWriter writes audit entries.
type AuditWriter interface {
	// Append writes e in tx, the transaction that makes the change. When it fails, return the
	// error so that the change is rolled back: a change that cannot be audited does not happen.
	Append(ctx context.Context, tx Tx, actor Actor, e AuditEntry) error
}

// Target is what an action is done to.
type Target struct {
	Kind string // such as "resource" or "product"
	ID   string
}

// Authorizer answers questions about permissions.
type Authorizer interface {
	// Actor returns who is making the request ctx belongs to; false outside an authenticated
	// request.
	Actor(ctx context.Context) (Actor, bool)
	// Allowed reports whether actor may do action to target. An error means the answer is not
	// known, and the module must refuse.
	Allowed(ctx context.Context, actor Actor, action string, target Target) (bool, error)
}

// Settings keeps the module's settings as JSON values under keys of its choosing.
type Settings interface {
	// Get returns the value under key; false when it was never set. An error means unknown,
	// never "not set".
	Get(ctx context.Context, key string) (json.RawMessage, bool, error)
	Set(ctx context.Context, key string, value json.RawMessage) error
}
