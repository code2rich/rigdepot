// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// auditLog stands in for package audit. It keeps every entry it was given in
// memory and writes a row per entry into test_audit in the caller's
// transaction, so that committed() counts only the entries whose transaction
// committed. It can be told to fail.
type auditLog struct {
	mu      sync.Mutex
	entries []access.AuditEntry
	fail    error
}

func (l *auditLog) Append(ctx context.Context, tx *dbx.Tx, e access.AuditEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail != nil {
		return l.fail
	}
	// Write a row in the transaction, so that the entries can be counted
	// in the database too: a rollback takes it back, like the real audit
	// log's row.
	if _, err := tx.ExecContext(ctx, `INSERT INTO test_audit (action) VALUES (?)`, string(e.Action)); err != nil {
		return err
	}
	l.entries = append(l.entries, e)
	return nil
}

func (l *auditLog) all() []access.AuditEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]access.AuditEntry(nil), l.entries...)
}

// committed counts the entries of an action that are in the database.
func (e *env) committed(t *testing.T, action access.Action) int {
	t.Helper()
	var n int
	if err := e.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM test_audit WHERE action = ?`, string(action)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (l *auditLog) last() access.AuditEntry {
	all := l.all()
	if len(all) == 0 {
		return access.AuditEntry{}
	}
	return all[len(all)-1]
}

// eventLog stands in for package events.
type eventLog struct {
	mu     sync.Mutex
	events []access.Event
}

func (l *eventLog) Publish(_ context.Context, e access.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) all() []access.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]access.Event(nil), l.events...)
}

// env is a migrated database with the service on it.
type env struct {
	db     *dbx.DB
	svc    *access.Service
	audit  *auditLog
	events *eventLog
}

func newEnv(t *testing.T, db *dbx.DB) *env {
	t.Helper()
	if _, err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE test_audit (action TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	e := &env{db: db, audit: &auditLog{}, events: &eventLog{}}
	e.svc = access.New(db, access.Options{Auditor: e.audit, Events: e.events})
	return e
}

// sqlite returns an env on a fresh SQLite database, for tests that are about
// logic rather than the SQL dialect.
func sqlite(t *testing.T) *env {
	return newEnv(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
}

// user creates an account directly, as the sign-in code would.
func (e *env) user(t *testing.T, username string, status access.Status, admin bool) access.User {
	t.Helper()
	var u access.User
	err := e.db.InTx(context.Background(), func(tx *dbx.Tx) error {
		var err error
		u, err = e.svc.CreateUser(context.Background(), tx, access.System, access.NewUser{
			Username: username, DisplayName: "Name of " + username, Email: username + "@example.com",
			Status: status, Admin: admin,
		})
		return err
	})
	if err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	if status == access.StatusDisabled {
		t.Fatal("create the user active and disable it with UpdateUser")
	}
	return u
}

func (e *env) product(t *testing.T, code string, owners ...string) access.Product {
	t.Helper()
	p, err := e.svc.CreateProduct(context.Background(), access.System, access.NewProduct{Code: code, Name: "Product " + code, Owners: owners})
	if err != nil {
		t.Fatalf("create product %s: %v", code, err)
	}
	return p
}

func (e *env) member(t *testing.T, code, userID string, role access.Role) {
	t.Helper()
	if _, _, err := e.svc.PutMember(context.Background(), access.System, code, userID, role); err != nil {
		t.Fatalf("put %s into %s as %s: %v", userID, code, role, err)
	}
}

func (e *env) disable(t *testing.T, userID string) {
	t.Helper()
	st := access.StatusDisabled
	if _, err := e.svc.UpdateUser(context.Background(), access.System, userID, access.UserChange{Status: &st}); err != nil {
		t.Fatalf("disable %s: %v", userID, err)
	}
}

func as(u access.User) access.Actor { return access.Actor{UserID: u.ID, Origin: access.OriginWeb} }

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: got %v, want %v", what, err, want)
	}
}

func must(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}
