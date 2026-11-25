// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package gitrepo_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/gitrepo"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.Run(m, func(env *testenv.Env) error {
		// The system configuration can name a credential helper (Apple's git ships with
		// osxkeychain), which would reach the real keychain. The global one is in the
		// temporary home anyway; naming it lets a test write it.
		if err := os.Setenv("GIT_CONFIG_NOSYSTEM", "1"); err != nil {
			return err
		}
		return os.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(env.Home, ".gitconfig"))
	}))
}

// repo is a fixture repository with a work tree, built with the real git.
type repo struct {
	t   *testing.T
	git string
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	r := &repo{t: t, git: testenv.NeedGit(t), dir: t.TempDir()}
	r.run("init", "--quiet", "--initial-branch=main")
	return r
}

func (r *repo) run(args ...string) string {
	r.t.Helper()
	cmd := exec.Command(r.git, append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=dev@example.com",
		"GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=dev@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *repo) write(name, content string, mode os.FileMode) {
	r.t.Helper()
	p := filepath.Join(r.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		r.t.Fatal(err)
	}
}

// commit stages everything and commits it; it returns the commit id.
func (r *repo) commit(msg string) string {
	r.t.Helper()
	r.run("add", "--all")
	r.run("-c", "core.autocrlf=false", "commit", "--quiet", "--message", msg)
	return r.run("rev-parse", "HEAD")
}

// bare clones the fixture into a bare repository and returns its file:// URL, the way a
// repository on a server looks.
func (r *repo) bare() string {
	r.t.Helper()
	dir := filepath.Join(r.t.TempDir(), "remote.git")
	r.run("clone", "--quiet", "--bare", r.dir, dir)
	return fileURL(dir)
}

func fileURL(dir string) string {
	p := filepath.ToSlash(dir)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows: file:///C:/…
	}
	return "file://" + p
}

func newFetcher(t *testing.T, opts gitrepo.Options) *gitrepo.Fetcher {
	t.Helper()
	if opts.Git == "" {
		opts.Git = testenv.NeedGit(t)
	}
	if opts.CacheDir == "" {
		opts.CacheDir = filepath.Join(t.TempDir(), "cache")
	}
	f, err := gitrepo.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func readFile(t *testing.T, fsys fs.FS, name string) string {
	t.Helper()
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSnapshotOfALocalBareRepository(t *testing.T) {
	r := newRepo(t)
	r.write(brand.RepoConfig, "schema: 1\n", 0o644)
	r.write("skills/unit-testing/SKILL.md", "---\nname: unit-testing\n---\nWrite tests first.\n", 0o644)
	r.write("skills/unit-testing/scripts/run.sh", "#!/bin/sh\necho ok\n", 0o755)
	commit := r.commit("first")
	url := r.bare()

	f := newFetcher(t, gitrepo.Options{AllowLocal: true})
	snap, err := f.Snapshot(context.Background(), gitrepo.Source{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Commit != commit {
		t.Errorf("Commit = %s, want %s", snap.Commit, commit)
	}
	if err := fstest.TestFS(snap.Files, brand.RepoConfig, "skills/unit-testing/SKILL.md", "skills/unit-testing/scripts/run.sh"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, snap.Files, "skills/unit-testing/SKILL.md"); got != "---\nname: unit-testing\n---\nWrite tests first.\n" {
		t.Errorf("SKILL.md = %q", got)
	}
	info, err := fs.Stat(snap.Files, "skills/unit-testing/scripts/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("run.sh mode = %v, want executable", info.Mode())
	}
	info, _ = fs.Stat(snap.Files, "skills/unit-testing/SKILL.md")
	if info.Mode()&0o111 != 0 {
		t.Errorf("SKILL.md mode = %v, want not executable", info.Mode())
	}
}

func TestSnapshotReadsNoWorkTree(t *testing.T) {
	r := newRepo(t)
	r.write("a.md", "a\n", 0o644)
	r.commit("first")
	url := r.bare()
	cache := filepath.Join(t.TempDir(), "cache")

	f := newFetcher(t, gitrepo.Options{AllowLocal: true, CacheDir: cache})
	if _, err := f.Snapshot(context.Background(), gitrepo.Source{URL: url}); err != nil {
		t.Fatal(err)
	}
	// The cache holds bare repositories and nothing else: no checked-out file anywhere.
	err := filepath.WalkDir(cache, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == "a.md" {
			t.Errorf("found a checked-out file at %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotOfBranchTagCommitAndDirectory(t *testing.T) {
	r := newRepo(t)
	r.write("skills/a/SKILL.md", "one\n", 0o644)
	first := r.commit("first")
	r.run("tag", "--annotate", "--message", "release", "v1")
	r.write("skills/a/SKILL.md", "two\n", 0o644)
	r.write("other/b.md", "b\n", 0o644)
	second := r.commit("second")
	r.run("checkout", "--quiet", "-b", "next")
	r.write("skills/a/SKILL.md", "three\n", 0o644)
	third := r.commit("third")
	r.run("checkout", "--quiet", "main")
	url := r.bare()

	f := newFetcher(t, gitrepo.Options{AllowLocal: true})
	cases := []struct {
		ref, want, content string
	}{
		{"", second, "two\n"}, // HEAD of the remote is main
		{"main", second, "two\n"},
		{"next", third, "three\n"},
		{"refs/heads/next", third, "three\n"},
		{"v1", first, "one\n"}, // an annotated tag is peeled to its commit
		{first, first, "one\n"},
	}
	for _, c := range cases {
		snap, err := f.Snapshot(context.Background(), gitrepo.Source{URL: url, Ref: c.ref, Dir: "skills"})
		if err != nil {
			t.Errorf("ref %q: %v", c.ref, err)
			continue
		}
		if snap.Commit != c.want {
			t.Errorf("ref %q: Commit = %s, want %s", c.ref, snap.Commit, c.want)
		}
		if got := readFile(t, snap.Files, "a/SKILL.md"); got != c.content {
			t.Errorf("ref %q: a/SKILL.md = %q, want %q", c.ref, got, c.content)
		}
		if _, err := fs.Stat(snap.Files, "other"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ref %q: the snapshot of skills/ contains other/ (err %v)", c.ref, err)
		}
	}

	for _, ref := range []string{"missing", "0123456789abcdef0123456789abcdef01234567"} {
		if _, err := f.Snapshot(context.Background(), gitrepo.Source{URL: url, Ref: ref}); !errors.Is(err, gitrepo.ErrNoRef) {
			t.Errorf("ref %q: err = %v, want ErrNoRef", ref, err)
		}
	}
	if _, err := f.Snapshot(context.Background(), gitrepo.Source{URL: url, Dir: "nothing/here"}); !errors.Is(err, gitrepo.ErrNoDir) {
		t.Errorf("missing directory: err = %v, want ErrNoDir", err)
	}
	if _, err := f.Snapshot(context.Background(), gitrepo.Source{URL: url, Dir: "skills/a/SKILL.md"}); !errors.Is(err, gitrepo.ErrNoDir) {
		t.Errorf("a file as directory: err = %v, want ErrNoDir", err)
	}
	for _, ref := range []string{"-c", "--upload-pack=x", "main..next", "main^", "a b", "main@{1}", "../x"} {
		if _, err := f.Snapshot(context.Background(), gitrepo.Source{URL: url, Ref: ref}); !errors.Is(err, gitrepo.ErrNoRef) {
			t.Errorf("ref %q: err = %v, want ErrNoRef", ref, err)
		}
	}
}

func TestSnapshotFollowsNewCommits(t *testing.T) {
	r := newRepo(t)
	r.write("a.md", "one\n", 0o644)
	r.commit("first")
	remote := filepath.Join(t.TempDir(), "remote.git")
	r.run("clone", "--quiet", "--bare", r.dir, remote)
	r.run("remote", "add", "origin", remote)

	f := newFetcher(t, gitrepo.Options{AllowLocal: true})
	if _, err := f.Snapshot(context.Background(), gitrepo.Source{URL: fileURL(remote)}); err != nil {
		t.Fatal(err)
	}
	r.write("a.md", "two\n", 0o644)
	second := r.commit("second")
	r.run("push", "--quiet", "origin", "main")

	snap, err := f.Snapshot(context.Background(), gitrepo.Source{URL: fileURL(remote)})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Commit != second || readFile(t, snap.Files, "a.md") != "two\n" {
		t.Errorf("second snapshot is at %s with %q, want %s with %q", snap.Commit, readFile(t, snap.Files, "a.md"), second, "two\n")
	}
}

// TestSnapshotIsTheStoredBytes: the same commit gives the same bytes whatever the person's git
// settings and the repository's attributes say about line endings and substitution.
func TestSnapshotIsTheStoredBytes(t *testing.T) {
	r := newRepo(t)
	r.write(".gitattributes", "* text eol=crlf\nsubst.md export-subst\nhidden.md export-ignore\n", 0o644)
	r.write("lf.md", "line one\nline two\n", 0o644)
	r.write("subst.md", "commit $Format:%H$\n", 0o644)
	r.write("hidden.md", "still here\n", 0o644)
	r.commit("first")
	url := r.bare()

	global := os.Getenv("GIT_CONFIG_GLOBAL")
	if err := os.WriteFile(global, []byte("[core]\n\tautocrlf = true\n\teol = crlf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(global) })

	f := newFetcher(t, gitrepo.Options{AllowLocal: true})
	snap, err := f.Snapshot(context.Background(), gitrepo.Source{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"lf.md":     "line one\nline two\n",
		"subst.md":  "commit $Format:%H$\n",
		"hidden.md": "still here\n",
	}
	for name, content := range want {
		if got := readFile(t, snap.Files, name); got != content {
			t.Errorf("%s = %q, want the stored %q", name, got, content)
		}
	}
}

// TestArchiveIgnoresLineEndingSettings: the tar stream Archive writes (what bundle reads) has the
// stored LF bytes even when the person's git is set to check out with CRLF, as is common on
// Windows. The repository has no .gitattributes, so only core.autocrlf and core.eol are at work.
func TestArchiveIgnoresLineEndingSettings(t *testing.T) {
	r := newRepo(t)
	r.write("skills/a/SKILL.md", "line one\nline two\n", 0o644)
	commit := r.commit("first")
	url := r.bare()

	global := os.Getenv("GIT_CONFIG_GLOBAL")
	if err := os.WriteFile(global, []byte("[core]\n\tautocrlf = true\n\teol = crlf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(global) })

	f := newFetcher(t, gitrepo.Options{AllowLocal: true})
	var out bytes.Buffer
	got, err := f.Archive(context.Background(), gitrepo.Source{URL: url, Dir: "skills"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if got != commit {
		t.Errorf("commit = %s, want %s", got, commit)
	}
	tr := tar.NewReader(&out)
	found := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name != "a/SKILL.md" {
			continue
		}
		found = true
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "line one\nline two\n" {
			t.Errorf("a/SKILL.md in the archive = %q, want the stored LF bytes", data)
		}
	}
	if !found {
		t.Error("a/SKILL.md is not in the archive")
	}
}

func TestSnapshotListsSymbolicLinksSeparately(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating the fixture needs symbolic links in the work tree")
	}
	r := newRepo(t)
	r.write("skills/a/SKILL.md", "a\n", 0o644)
	if err := os.Symlink("../../../etc/passwd", filepath.Join(r.dir, "skills", "a", "link")); err != nil {
		t.Fatal(err)
	}
	r.commit("first")

	f := newFetcher(t, gitrepo.Options{AllowLocal: true})
	snap, err := f.Snapshot(context.Background(), gitrepo.Source{URL: r.bare()})
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Links["skills/a/link"]; got != "../../../etc/passwd" {
		t.Errorf("Links = %v, want skills/a/link -> ../../../etc/passwd", snap.Links)
	}
	if _, err := fs.Stat(snap.Files, "skills/a/link"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the link is a file of the snapshot (err %v)", err)
	}
}

func TestSnapshotTooLarge(t *testing.T) {
	r := newRepo(t)
	r.write("big.md", strings.Repeat("x", 64<<10), 0o644)
	r.commit("first")

	f := newFetcher(t, gitrepo.Options{AllowLocal: true, MaxBytes: 16 << 10})
	if _, err := f.Snapshot(context.Background(), gitrepo.Source{URL: r.bare()}); !errors.Is(err, gitrepo.ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}

func TestLocalRepositoriesOnlyWhenAllowed(t *testing.T) {
	r := newRepo(t)
	r.write("a.md", "a\n", 0o644)
	r.commit("first")
	url := r.bare()

	f := newFetcher(t, gitrepo.Options{})
	for _, addr := range []string{url, r.dir, "."} {
		if _, err := f.Snapshot(context.Background(), gitrepo.Source{URL: addr}); !errors.Is(err, gitrepo.ErrAddress) {
			t.Errorf("%q without AllowLocal: err = %v, want ErrAddress", addr, err)
		}
	}
	// A plain local path (a work tree, not only a bare repository) works when allowed.
	f = newFetcher(t, gitrepo.Options{AllowLocal: true})
	snap, err := f.Snapshot(context.Background(), gitrepo.Source{URL: r.dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, snap.Files, "a.md"); got != "a\n" {
		t.Errorf("a.md = %q", got)
	}
}

func TestNoGit(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-git")
	_, err := gitrepo.New(gitrepo.Options{Git: missing, CacheDir: t.TempDir()})
	if !errors.Is(err, gitrepo.ErrNoGit) {
		t.Fatalf("err = %v, want ErrNoGit", err)
	}
	for _, want := range []string{missing, "install Git", brand.EnvPrefix + "GIT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %q", err, want)
		}
	}

	t.Setenv("PATH", t.TempDir())
	_, err = gitrepo.New(gitrepo.Options{CacheDir: t.TempDir()})
	if !errors.Is(err, gitrepo.ErrNoGit) {
		t.Fatalf("with no git on PATH: err = %v, want ErrNoGit", err)
	}
	for _, want := range []string{"PATH", "install Git", brand.EnvPrefix + "GIT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %q", err, want)
		}
	}
}

// askpassScript writes a program that records that it was asked and answers, the way an IDE's
// or a desktop's credential dialog would. It returns the program and the file it records into.
func askpassScript(t *testing.T) (program, record string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in prompt program is a shell script")
	}
	dir := t.TempDir()
	record = filepath.Join(dir, "asked")
	program = filepath.Join(dir, "askpass")
	script := "#!/bin/sh\necho \"$1\" >> '" + record + "'\necho secret\n"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return program, record
}

// TestNoCredentialPrompt: a repository that wants a password makes the snapshot fail, and
// nothing is asked: not on the terminal, not through an askpass program the person's
// environment names.
func TestNoCredentialPrompt(t *testing.T) {
	program, record := askpassScript(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="example"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	t.Setenv("GIT_SSL_NO_VERIFY", "1") // the test server's certificate is self-signed
	t.Setenv("GIT_ASKPASS", program)
	t.Setenv("SSH_ASKPASS", program)
	t.Setenv("GIT_TERMINAL_PROMPT", "1")

	f := newFetcher(t, gitrepo.Options{Timeout: 30 * time.Second})
	_, err := f.Snapshot(context.Background(), gitrepo.Source{URL: srv.URL + "/example/resources.git"})
	if !errors.Is(err, gitrepo.ErrFetch) {
		t.Errorf("err = %v, want ErrFetch", err)
	}
	if asked, err := os.ReadFile(record); err == nil {
		t.Errorf("git asked for credentials:\n%s", asked)
	}
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	t.Setenv("GIT_SSL_NO_VERIFY", "1")

	f := newFetcher(t, gitrepo.Options{Timeout: time.Second})
	start := time.Now()
	_, err := f.Snapshot(context.Background(), gitrepo.Source{URL: srv.URL + "/example/resources.git"})
	if !errors.Is(err, gitrepo.ErrTimeout) {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Errorf("the snapshot took %s to give up", took)
	}
}

func TestBrokenCacheIsRebuilt(t *testing.T) {
	r := newRepo(t)
	r.write("a.md", "a\n", 0o644)
	r.commit("first")
	url := r.bare()
	cache := filepath.Join(t.TempDir(), "cache")
	f := newFetcher(t, gitrepo.Options{AllowLocal: true, CacheDir: cache})
	if _, err := f.Snapshot(context.Background(), gitrepo.Source{URL: url}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cache entries = %v, %v; want one repository", entries, err)
	}
	repo := filepath.Join(cache, entries[0].Name())
	if err := os.RemoveAll(filepath.Join(repo, "objects")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "HEAD")); err != nil {
		t.Fatal(err)
	}
	snap, err := f.Snapshot(context.Background(), gitrepo.Source{URL: url})
	if err != nil {
		t.Fatalf("after damaging the cache: %v", err)
	}
	if got := readFile(t, snap.Files, "a.md"); got != "a\n" {
		t.Errorf("a.md = %q", got)
	}
}

func TestEnvironmentPointingElsewhereIsIgnored(t *testing.T) {
	r := newRepo(t)
	r.write("a.md", "a\n", 0o644)
	r.commit("first")
	url := r.bare()
	other := newRepo(t)
	other.write("other.md", "other\n", 0o644)
	other.commit("other")
	// As if rgd had been started from a git hook of another repository.
	t.Setenv("GIT_DIR", filepath.Join(other.dir, ".git"))
	t.Setenv("GIT_WORK_TREE", other.dir)

	f := newFetcher(t, gitrepo.Options{AllowLocal: true})
	snap, err := f.Snapshot(context.Background(), gitrepo.Source{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, snap.Files, "a.md"); got != "a\n" {
		t.Errorf("a.md = %q", got)
	}
}

func TestVersion(t *testing.T) {
	f := newFetcher(t, gitrepo.Options{})
	v, err := f.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v, "git version ") {
		t.Errorf("Version = %q", v)
	}
}
