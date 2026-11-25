// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/code2rich/rigdepot/internal/repoformat"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/catalog"
	"github.com/code2rich/rigdepot/internal/server/catalog/catalogtest"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/events"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// recorder keeps the events published after their transaction committed.
type recorder struct {
	mu  sync.Mutex
	got []events.Payload
}

func (r *recorder) Publish(tx events.Tx, p events.Payload) {
	tx.AfterCommit(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.got = append(r.got, p)
	})
}

func (r *recorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, p := range r.got {
		out = append(out, p.EventName())
	}
	return out
}

// fixture is the catalog on a fresh database with a cast: admin (platform administrator),
// owner (of product shop), member (of shop), outsider (in no product).
type fixture struct {
	t      *testing.T
	db     *dbx.DB
	acc    *access.Service
	log    *audit.Log
	svc    *catalog.Service
	events *recorder
	users  map[string]access.User
	source string
}

func newFixture(t *testing.T, db *dbx.DB) *fixture {
	t.Helper()
	ctx := context.Background()
	if _, err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	log, err := audit.New(db, make([]byte, audit.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	acc := access.New(db, access.Options{Auditor: accessAuditor{log}})
	f := &fixture{t: t, db: db, acc: acc, log: log, events: &recorder{}, users: map[string]access.User{}}
	for _, c := range []struct {
		name  string
		admin bool
	}{{"admin", true}, {"owner", false}, {"member", false}, {"outsider", false}} {
		err := db.InTx(ctx, func(tx *dbx.Tx) error {
			u, err := acc.CreateUser(ctx, tx, access.System, access.NewUser{Username: c.name, DisplayName: "Name of " + c.name,
				Email: c.name + "@example.com", Status: access.StatusActive, Admin: c.admin})
			f.users[c.name] = u
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := acc.CreateProduct(ctx, access.System, access.NewProduct{Code: "shop", Name: "Shop", Owners: []string{f.users["owner"].ID}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := acc.PutMember(ctx, access.System, "shop", f.users["member"].ID, access.RoleMember); err != nil {
		t.Fatal(err)
	}
	f.source = dbx.NewID()
	if _, err := db.ExecContext(ctx, `INSERT INTO sources (id, name, url, created_at, updated_at) VALUES (?, 'harness-resources', 'https://git.example.com/r.git', 1, 1)`, f.source); err != nil {
		t.Fatal(err)
	}
	f.svc, err = catalog.New(db, acc, catalog.Options{Auditor: log, Events: f.events, Kinds: catalogtest.Kinds(), Rules: catalogtest.Rules()})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func newSQLite(t *testing.T) *fixture { return newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t))) }

// accessAuditor puts access's entries into the same log (what the server's assembly does).
type accessAuditor struct{ log *audit.Log }

func (a accessAuditor) Append(ctx context.Context, tx *dbx.Tx, e access.AuditEntry) error {
	_, err := a.log.Append(ctx, tx, audit.Entry{Actor: audit.Actor{UserID: e.Actor.UserID, Origin: audit.Origin(e.Actor.Origin)},
		Action: string(e.Action), Object: audit.Object{Kind: e.ObjectKind, ID: e.ObjectID, Label: e.ObjectLabel}, Change: e.Change,
		Result: audit.Result(e.Result), Reason: e.Reason})
	return err
}

func (f *fixture) as(name string) access.Actor {
	return access.Actor{UserID: f.users[name].ID, Origin: access.OriginWeb}
}

const config = `schema: 1
products: [{code: shop}]
secrets:
  JIRA_TOKEN: {label: Jira token}
`

// repo writes files into a directory and reads it as a resource repository.
func (f *fixture) repo(files map[string]string) *repoformat.Repo {
	f.t.Helper()
	dir := f.t.TempDir()
	if _, ok := files["rigdepot.yaml"]; !ok {
		files["rigdepot.yaml"] = config
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	r, err := repoformat.Read(os.DirFS(dir), repoformat.Options{Kinds: catalogtest.Kinds()})
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) importRepo(files map[string]string, commit string) catalog.ImportResult {
	f.t.Helper()
	res, err := f.svc.ImportRepo(context.Background(), access.System, catalog.ImportSource{SourceID: f.source, Commit: commit}, f.repo(files))
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

// auditCount counts the entries of an action that ended with result.
func (f *fixture) auditCount(action string, result audit.Result) int {
	f.t.Helper()
	page, err := f.log.Query(context.Background(), audit.Filter{Action: action, Result: result}, audit.MaxLimit, "")
	if err != nil {
		f.t.Fatal(err)
	}
	return len(page.Rows)
}
