// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package delivery_test

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/repoformat"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/catalog"
	"github.com/code2rich/rigdepot/internal/server/catalog/catalogtest"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/delivery"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// fixture is delivery on a fresh database with the catalog's test kinds and a cast: admin
// (platform administrator), owner (of product shop), member (of shop), outsider (in no
// product).
type fixture struct {
	t      *testing.T
	db     *dbx.DB
	acc    *access.Service
	log    *audit.Log
	cat    *catalog.Service
	svc    *delivery.Service
	users  map[string]access.User
	source string
	// live are the signed-in devices handed to delivery (Options.LiveDevices).
	live map[string]string
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
	f := &fixture{t: t, db: db, acc: acc, log: log, users: map[string]access.User{}, live: map[string]string{}}
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
	f.cat, err = catalog.New(db, acc, catalog.Options{Auditor: log, Kinds: catalogtest.Kinds(), Rules: catalogtest.Rules()})
	if err != nil {
		t.Fatal(err)
	}
	f.svc = delivery.New(db, acc, f.cat, delivery.Options{Auditor: log, LiveDevices: func(context.Context) (map[string]string, error) {
		return f.live, nil
	}})
	return f
}

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

// device signs a computer in for a person, the way authn's devices table has it, and returns
// the actor of its device token.
func (f *fixture) device(name, computer string) access.Actor {
	f.t.Helper()
	id := dbx.NewID()
	now := dbx.Millis(time.Now())
	if _, err := f.db.ExecContext(context.Background(), `INSERT INTO devices (id, user_id, name, os, public_key, token_digest, created_at, last_seen_at, issued_at)
		VALUES (?, ?, ?, 'linux', 'key', ?, ?, ?, ?)`, id, f.users[name].ID, computer, "digest-"+id, now, now, now); err != nil {
		f.t.Fatal(err)
	}
	f.live[id] = f.users[name].ID
	return access.Actor{UserID: f.users[name].ID, Origin: access.OriginCLI, DeviceID: id}
}

const repoConfig = `schema: 1
products: [{code: shop}]
secrets:
  JIRA_TOKEN: {label: Jira token}
`

// importAndPublish imports a repository of files and publishes every resource in it.
func (f *fixture) importAndPublish(files map[string]string) {
	f.t.Helper()
	ctx := context.Background()
	dir := f.t.TempDir()
	files["rigdepot.yaml"] = repoConfig
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	repo, err := repoformat.Read(os.DirFS(dir), repoformat.Options{Kinds: catalogtest.Kinds()})
	if err != nil {
		f.t.Fatal(err)
	}
	if len(repo.Failed) > 0 {
		f.t.Fatalf("the repository has failures: %+v", repo.Failed)
	}
	res, err := f.cat.ImportRepo(ctx, access.System, catalog.ImportSource{SourceID: f.source, Commit: "c1"}, repo)
	if err != nil {
		f.t.Fatal(err)
	}
	if len(res.Failed) > 0 {
		f.t.Fatalf("import failures: %+v", res.Failed)
	}
	for _, r := range repo.Resources {
		f.publish(f.as("admin"), r.Ref.Code)
	}
}

func (f *fixture) publish(a access.Actor, code string) {
	f.t.Helper()
	ctx := context.Background()
	d, err := f.cat.Draft(ctx, a, code)
	if err != nil {
		f.t.Fatalf("draft of %s: %v", code, err)
	}
	if _, _, err := f.cat.Publish(ctx, a, code, d.ETag(), ""); err != nil {
		f.t.Fatalf("publish %s: %v", code, err)
	}
}

// personal uploads and publishes a personal skill of the named person.
func (f *fixture) personal(name, code, policy string) {
	f.t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for file, data := range map[string]string{
		"SKILL.md":  "---\nname: " + code + "\ndescription: Mine.\n---\nSteps.\n",
		"meta.yaml": "name: " + code + "\nwhen: Always\nscope: me\npolicy: " + policy + "\n",
	} {
		w, _ := zw.Create(file)
		w.Write([]byte(data))
	}
	zw.Close()
	if _, err := f.cat.Upload(context.Background(), f.as(name), code, "skill", buf.Bytes(), nil); err != nil {
		f.t.Fatalf("upload %s: %v", code, err)
	}
	f.publish(f.as(name), code)
}

// skill is a skill of a repository with the given scope and policy.
func skill(code, scope, policy string) map[string]string {
	return map[string]string{
		"skills/" + code + "/SKILL.md":  "---\nname: " + code + "\ndescription: Does " + code + ".\n---\nSteps.\n",
		"skills/" + code + "/meta.yaml": "name: " + code + "\nwhen: When needed\nscope: " + scope + "\npolicy: " + policy + "\n",
	}
}

// workflow is a workflow of a repository whose steps use the given resources.
func workflow(code, scope, policy string, uses ...string) map[string]string {
	doc := "meta: {name: " + code + ", when: When shipping, scope: " + scope + ", policy: " + policy + "}\nsteps:\n"
	for _, u := range uses {
		doc += "  - use: " + u + "\n"
	}
	return map[string]string{"workflows/" + code + ".yaml": doc}
}

func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
