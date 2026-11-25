// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/code2rich/rigdepot/internal/gitrepo"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/catalog"
	"github.com/code2rich/rigdepot/internal/server/catalog/catalogtest"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// gitRepo is a repository on this computer, read through its file:// address the way the
// server reads a remote one.
type gitRepo struct {
	t   *testing.T
	git string
	dir string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	r := &gitRepo{t: t, git: testenv.NeedGit(t), dir: t.TempDir()}
	r.run("init", "--quiet", "--initial-branch=main")
	return r
}

func (r *gitRepo) run(args ...string) string {
	r.t.Helper()
	cmd := exec.Command(r.git, append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=dev@example.com",
		"GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=dev@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit makes the work tree exactly files (plus rigdepot.yaml) and commits it.
func (r *gitRepo) commit(files map[string]string) string {
	r.t.Helper()
	entries, _ := os.ReadDir(r.dir)
	for _, e := range entries {
		if e.Name() != ".git" {
			os.RemoveAll(filepath.Join(r.dir, e.Name()))
		}
	}
	if _, ok := files["rigdepot.yaml"]; !ok {
		files["rigdepot.yaml"] = config
	}
	for name, content := range files {
		p := filepath.Join(r.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.run("add", "--all")
	r.run("-c", "core.autocrlf=false", "commit", "--quiet", "--allow-empty", "--message", "change")
	return r.run("rev-parse", "HEAD")
}

func (r *gitRepo) url() string {
	p := filepath.ToSlash(r.dir)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file://" + p
}

// sourcesFixture is the catalog with a master key, local sources allowed and the real fetcher.
type sourcesFixture struct {
	*fixture
	keys    *masterkey.Keyring
	fetcher catalog.Fetcher
	pulls   atomic.Int32 // how often PullNow was called
}

func newSourcesFixture(t *testing.T, db *dbx.DB) *sourcesFixture {
	t.Helper()
	f := &sourcesFixture{fixture: newFixture(t, db)}
	// The plain fixture's source has an address nobody may reach from a test.
	if _, err := db.ExecContext(context.Background(), `DELETE FROM sources WHERE id = ?`, f.fixture.source); err != nil {
		t.Fatal(err)
	}
	var err error
	if f.keys, err = masterkey.New(bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatal(err)
	}
	f.svc, err = catalog.New(db, f.acc, catalog.Options{Auditor: f.log, Events: f.events, Kinds: catalogtest.Kinds(), Rules: catalogtest.Rules(),
		Keys: f.keys, AllowLocalSources: true, PullNow: func() { f.pulls.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := gitrepo.New(gitrepo.Options{Git: testenv.NeedGit(t), CacheDir: filepath.Join(t.TempDir(), "repos"), AllowLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	f.fetcher = fetcher
	return f
}

func (f *sourcesFixture) create(in catalog.SourceInput) catalog.Source {
	f.t.Helper()
	src, err := f.svc.CreateSource(context.Background(), f.as("admin"), in)
	if err != nil {
		f.t.Fatal(err)
	}
	return src
}

func (f *sourcesFixture) source(id string) catalog.Source {
	f.t.Helper()
	src, err := f.svc.Source(context.Background(), f.as("admin"), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return src
}

// pullAll runs the source-sync job once.
func (f *sourcesFixture) pullAll() error {
	return f.svc.PullSources(context.Background(), f.fetcher)
}

func TestPullImportsAndIsIdempotent(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := newSourcesFixture(t, db)
		ctx := context.Background()
		repo := newGitRepo(t)
		c1 := repo.commit(merge(skill("unit-testing", "Steps.\n", ""), skill("code-review", "Review.\n", "")))
		src := f.create(catalog.SourceInput{Name: "harness-resources", URL: repo.url(), Branch: "main"})
		if src.Order != 1 || src.LastPull == nil || src.LastPull.Result != catalog.PullRunning || f.pulls.Load() != 1 {
			t.Fatalf("a new source asks for its first pull: %+v (%d triggers)", src, f.pulls.Load())
		}
		if err := f.pullAll(); err != nil {
			t.Fatal(err)
		}
		src = f.source(src.ID)
		if p := src.LastPull; p == nil || p.Result != catalog.PullOK || p.Commit != c1 || p.Imported != 2 || p.Failed != 0 || p.LastSuccessAt.IsZero() {
			t.Fatalf("after the pull: %+v", p)
		}
		if src.ResourceCount != 2 {
			t.Errorf("resource count = %d", src.ResourceCount)
		}
		r, err := f.svc.Get(ctx, f.as("admin"), "unit-testing")
		if err != nil || r.SourceID != src.ID || r.DraftCommit != c1 || !r.HasDraft {
			t.Fatalf("imported resource: %v %+v", err, r)
		}

		// Pulling the same commit again changes nothing: no audit entry, no event.
		entries := f.auditCount("resource.", "")
		evs := len(f.events.names())
		if _, err := f.svc.RequestPull(ctx, f.as("admin"), src.ID); err != nil {
			t.Fatal(err)
		}
		if err := f.pullAll(); err != nil {
			t.Fatal(err)
		}
		if n := f.auditCount("resource.", ""); n != entries {
			t.Errorf("pulling the same commit wrote %d audit entries about resources", n-entries)
		}
		if n := len(f.events.names()); n != evs {
			t.Errorf("pulling the same commit published %d events", n-evs)
		}
		if p := f.source(src.ID).LastPull; p.Result != catalog.PullOK || p.Imported != 0 {
			t.Errorf("second pull: %+v", p)
		}
		// Not due and not asked for: the job leaves it alone.
		before := f.source(src.ID).LastPull.At
		if err := f.pullAll(); err != nil {
			t.Fatal(err)
		}
		if after := f.source(src.ID).LastPull.At; !after.Equal(before) {
			t.Errorf("a source pulled a moment ago was pulled again")
		}
	})
}

func TestPullFailureKeepsEverything(t *testing.T) {
	f := newSourcesFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	ctx := context.Background()
	repo := newGitRepo(t)
	c1 := repo.commit(merge(skill("unit-testing", "Steps.\n", ""), skill("code-review", "Review.\n", "")))
	src := f.create(catalog.SourceInput{Name: "harness-resources", URL: repo.url(), Branch: "main"})
	if err := f.pullAll(); err != nil {
		t.Fatal(err)
	}
	d, _ := f.svc.Draft(ctx, f.as("admin"), "code-review")
	if _, _, err := f.svc.Publish(ctx, f.as("admin"), "code-review", d.ETag(), ""); err != nil {
		t.Fatal(err)
	}
	first := f.source(src.ID).LastPull

	// One resource breaks, one is removed: the other is imported, nothing is deleted.
	c2 := repo.commit(merge(skill("unit-testing", "Steps, changed.\n", ""), map[string]string{"skills/code-review/SKILL.md": "---\nname: code-review\n---\n"}))
	if _, err := f.svc.RequestPull(ctx, f.as("admin"), src.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.pullAll(); err != nil {
		t.Fatal(err)
	}
	p := f.source(src.ID).LastPull
	if p.Result != catalog.PullOK || p.Commit != c2 || p.Imported != 1 || p.Failed != 1 {
		t.Fatalf("a pull with a broken resource: %+v", p)
	}
	if r, err := f.svc.Get(ctx, f.as("admin"), "code-review"); err != nil || r.Version != 1 || r.Status != catalog.StatusPublished {
		t.Errorf("code-review after its metadata broke: %v %+v", err, r)
	}
	repo.commit(skill("unit-testing", "Steps, changed.\n", ""))
	f.svc.RequestPull(ctx, f.as("admin"), src.ID)
	if err := f.pullAll(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Get(ctx, f.as("admin"), "code-review"); err != nil {
		t.Errorf("code-review left the repository and was removed: %v", err)
	}
	good := f.source(src.ID).LastPull

	// The repository cannot be read at all: the pull fails, the last good state stays.
	resources, _ := f.svc.List(ctx, f.as("admin"), catalog.Filter{})
	if err := os.RemoveAll(filepath.Join(repo.dir, ".git")); err != nil {
		t.Fatal(err)
	}
	f.svc.RequestPull(ctx, f.as("admin"), src.ID)
	err := f.pullAll()
	if err == nil || !strings.Contains(err.Error(), "harness-resources") {
		t.Errorf("the job's error does not name the source: %v", err)
	}
	p = f.source(src.ID).LastPull
	if p.Result != catalog.PullFailed || p.Error == "" || p.Commit != good.Commit || !p.LastSuccessAt.Equal(good.LastSuccessAt) {
		t.Fatalf("a failed pull: %+v (last good %+v)", p, good)
	}
	after, _ := f.svc.List(ctx, f.as("admin"), catalog.Filter{})
	if len(after) != len(resources) {
		t.Errorf("a failed pull changed the catalog: %d resources, had %d", len(after), len(resources))
	}
	failing, err := f.svc.SourcesFailing(ctx)
	if err != nil || !slices.Equal(failing, []string{"harness-resources"}) {
		t.Errorf("failing sources: %v %v", failing, err)
	}
	_ = first
	_ = c1
}

func TestSourceCredentialIsSealedAndNeverShown(t *testing.T) {
	const token = "<placeholder-read-only-token>"
	f := newSourcesFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	ctx := context.Background()
	src := f.create(catalog.SourceInput{Name: "remote", URL: "https://git.example.com/team/resources.git", Branch: "main", Credential: token})
	if !src.HasCredential {
		t.Fatal("has_credential is false")
	}
	var stored string
	if err := f.db.QueryRowContext(ctx, `SELECT credential FROM sources WHERE id = ?`, src.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "" || strings.Contains(stored, token) {
		t.Fatalf("credential column: %q", stored)
	}
	// The pull hands the token to the fetcher, and masks it in what it keeps of a failure.
	fake := &fakeFetcher{err: errors.New("proxy said: bad token " + token)}
	if err := f.svc.PullSources(ctx, fake); err == nil {
		t.Error("a failed pull is not reported")
	}
	if len(fake.got) != 1 || fake.got[0].Credential == nil || fake.got[0].Credential.Password != token {
		t.Fatalf("fetcher got %+v", fake.got)
	}
	p := f.source(src.ID).LastPull
	if p.Result != catalog.PullFailed || strings.Contains(p.Error, token) || !strings.Contains(p.Error, "xxxxx") {
		t.Errorf("failure kept as %q", p.Error)
	}
	page, _ := f.log.Query(ctx, audit.Filter{Action: "source."}, audit.MaxLimit, "")
	for _, row := range page.Rows {
		if strings.Contains(string(mustJSON(row)), token) {
			t.Errorf("the audit log holds the token: %+v", row)
		}
	}

	// Rotating the master key rewraps it; it still opens.
	rotated, err := masterkey.New(bytes.Repeat([]byte{9}, 32), bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	counts, err := masterkey.RewrapAll(ctx, f.db, rotated, []masterkey.Rewrapper{catalog.SourcesRewrapper})
	if err != nil || counts["sources"] != 1 {
		t.Fatalf("rewrap: %v %v", counts, err)
	}
	f.svc, _ = catalog.New(f.db, f.acc, catalog.Options{Auditor: f.log, Kinds: catalogtest.Kinds(), Rules: catalogtest.Rules(), Keys: rotated})
	fake.got = nil
	f.svc.RequestPull(ctx, f.as("admin"), src.ID)
	f.svc.PullSources(ctx, fake)
	if len(fake.got) != 1 || fake.got[0].Credential == nil || fake.got[0].Credential.Password != token {
		t.Errorf("after rotation the fetcher got %+v", fake.got)
	}

	// Clearing it, and refusing one for ssh.
	cur := f.source(src.ID)
	cleared, err := f.svc.UpdateSource(ctx, f.as("admin"), src.ID, ptr(cur.ETag()), catalog.SourceChange{ClearCredential: true})
	if err != nil || cleared.HasCredential {
		t.Fatalf("clear: %v %+v", err, cleared)
	}
	_, err = f.svc.CreateSource(ctx, f.as("admin"), catalog.SourceInput{Name: "ssh", URL: "git@git.example.com:team/r.git", Branch: "main", Credential: token})
	assertField(t, err, "credential", "unsupported")
}

type fakeFetcher struct {
	got []gitrepo.Source
	err error
}

func (f *fakeFetcher) Snapshot(_ context.Context, src gitrepo.Source) (*gitrepo.Snapshot, error) {
	f.got = append(f.got, src)
	return nil, f.err
}

func assertField(t *testing.T, err error, field, code string) {
	t.Helper()
	var ce *catalog.Error
	if !errors.As(err, &ce) || ce.Kind != catalog.KindInvalid || len(ce.Fields) == 0 || ce.Fields[0].Field != field || ce.Fields[0].Code != code {
		t.Errorf("err = %v, want %s %s", err, field, code)
	}
}

func ptr[T any](v T) *T { return &v }

func TestSourceValidationAndPermissions(t *testing.T) {
	f := newSourcesFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	ctx := context.Background()
	in := func(url string) catalog.SourceInput { return catalog.SourceInput{Name: "r", URL: url, Branch: "main"} }
	_, err := f.svc.CreateSource(ctx, f.as("admin"), in("http://git.example.com/r.git"))
	assertField(t, err, "url", "invalid")
	_, err = f.svc.CreateSource(ctx, f.as("admin"), in("https://user:pw@git.example.com/r.git"))
	assertField(t, err, "url", "has_password")
	_, err = f.svc.CreateSource(ctx, f.as("admin"), catalog.SourceInput{Name: "r", URL: "https://git.example.com/r.git", Branch: "main", Dir: "../up"})
	assertField(t, err, "dir", "invalid")

	// Without AllowLocalSources the server does not read its own disk.
	strict, _ := catalog.New(f.db, f.acc, catalog.Options{Auditor: f.log, Kinds: catalogtest.Kinds(), Rules: catalogtest.Rules()})
	_, err = strict.CreateSource(ctx, f.as("admin"), in("file:///srv/resources.git"))
	assertField(t, err, "url", "invalid")
	_, err = strict.CreateSource(ctx, f.as("admin"), catalog.SourceInput{Name: "r", URL: "https://git.example.com/r.git", Branch: "main", Credential: "x"})
	if !errors.Is(err, catalog.ErrNoMasterKey) {
		t.Errorf("a credential without a master key: %v", err)
	}

	// Only platform administrators; a refused write is in the audit log.
	if _, err := f.svc.CreateSource(ctx, f.as("owner"), in("https://git.example.com/r.git")); !errors.Is(err, catalog.ErrDenied) {
		t.Errorf("owner creates a source: %v", err)
	}
	if _, err := f.svc.Sources(ctx, f.as("member")); !errors.Is(err, catalog.ErrDenied) {
		t.Errorf("member lists sources: %v", err)
	}
	if f.auditCount("source.manage", audit.ResultDenied) != 1 {
		t.Error("the refused create is not in the audit log")
	}
	if _, err := f.svc.Source(ctx, f.as("admin"), dbx.NewID()); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("unknown source: %v", err)
	}
}

func TestSourceOrderUpdateAndDelete(t *testing.T) {
	f := newSourcesFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	ctx := context.Background()
	repo := newGitRepo(t)
	repo.commit(skill("unit-testing", "Steps.\n", ""))
	a := f.create(catalog.SourceInput{Name: "a", URL: repo.url(), Branch: "main"})
	// b is not a repository: pulling it fails, on this computer.
	b := f.create(catalog.SourceInput{Name: "b", URL: (&gitRepo{dir: t.TempDir()}).url(), Branch: "main"})
	if b.Order != 2 {
		t.Fatalf("b.Order = %d", b.Order)
	}
	if _, err := f.svc.OrderSources(ctx, f.as("admin"), []string{b.ID}); err == nil {
		t.Error("an order that leaves a source out is accepted")
	}
	got, err := f.svc.OrderSources(ctx, f.as("admin"), []string{b.ID, a.ID})
	if err != nil || got[0].ID != b.ID || got[0].Order != 1 || got[1].Order != 2 {
		t.Fatalf("order: %v %+v", err, got)
	}

	// If-Match, and a change of address asks for a pull.
	_, err = f.svc.UpdateSource(ctx, f.as("admin"), b.ID, nil, catalog.SourceChange{Name: ptr("bee")})
	mustErr(t, err, catalog.ErrPreconditionRequired)
	_, err = f.svc.UpdateSource(ctx, f.as("admin"), b.ID, ptr(b.ETag()), catalog.SourceChange{Name: ptr("bee")})
	mustErr(t, err, catalog.ErrPreconditionFailed) // the order changed it
	b = f.source(b.ID)
	triggers := f.pulls.Load()
	renamed, err := f.svc.UpdateSource(ctx, f.as("admin"), b.ID, ptr(b.ETag()), catalog.SourceChange{Name: ptr("bee")})
	if err != nil || renamed.Name != "bee" || renamed.ETag() == b.ETag() || f.pulls.Load() != triggers {
		t.Fatalf("rename: %v %+v", err, renamed)
	}
	moved, err := f.svc.UpdateSource(ctx, f.as("admin"), b.ID, ptr(renamed.ETag()), catalog.SourceChange{Branch: ptr("release")})
	if err != nil || moved.Branch != "release" || f.pulls.Load() != triggers+1 {
		t.Fatalf("change branch: %v %+v", err, moved)
	}

	// Deleting a source keeps its resources; another source can take them over.
	if err := f.pullAll(); err == nil || !strings.Contains(err.Error(), "bee") {
		t.Errorf("pulling a directory that is not a repository: %v", err)
	}
	if r, err := f.svc.Get(ctx, f.as("admin"), "unit-testing"); err != nil || r.SourceID != a.ID {
		t.Fatalf("before delete: %v %+v", err, r)
	}
	if err := f.svc.DeleteSource(ctx, f.as("admin"), a.ID); err != nil {
		t.Fatal(err)
	}
	r, err := f.svc.Get(ctx, f.as("admin"), "unit-testing")
	if err != nil || r.SourceID != "" || !r.HasDraft || r.Origin != catalog.OriginRepo {
		t.Fatalf("after delete: %v %+v", err, r)
	}
	if left, _ := f.svc.Sources(ctx, f.as("admin")); len(left) != 1 || left[0].Order != 1 {
		t.Errorf("sources after delete: %+v", left)
	}
	c := f.create(catalog.SourceInput{Name: "c", URL: repo.url(), Branch: "main"})
	f.svc.PullSources(ctx, f.fetcher) // b still fails; c imports
	if r, err := f.svc.Get(ctx, f.as("admin"), "unit-testing"); err != nil || r.SourceID != c.ID {
		t.Errorf("not adopted by the new source: %v %+v", err, r)
	}
	if p := f.source(c.ID).LastPull; p.Result != catalog.PullOK {
		t.Errorf("c: %+v", p)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
