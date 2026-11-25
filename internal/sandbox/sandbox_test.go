// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sandbox_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/sandbox"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

var credentials = []string{".credentials.json", "auth.json"}

// world is a home directory that stands for the person's real one in a test.
type world struct {
	t    *testing.T
	home string
	tmp  string // the local data directory's tmp/
}

func newWorld(t *testing.T) *world {
	t.Helper()
	home := t.TempDir()
	return &world{t: t, home: home, tmp: filepath.Join(home, brand.Home, "tmp")}
}

func (w *world) opts() sandbox.Options {
	return sandbox.Options{TmpDir: w.tmp, Home: w.home, Credentials: credentials}
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (w *world) write(rel, content string) { w.t.Helper(); write(w.t, w.path(rel), content) }
func (w *world) path(rel string) string    { return filepath.Join(w.home, filepath.FromSlash(rel)) }

// tree returns every path under root (except the local data directory) with the content of
// files, to check that something was left alone.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if rel == brand.Home {
			return filepath.SkipDir
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			out[filepath.ToSlash(rel)] = "-> " + target
		case d.IsDir():
			out[filepath.ToSlash(rel)+"/"] = ""
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[filepath.ToSlash(rel)] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// fakeExecutor stands in for internal/executor: it carries out the operations inside the
// sandbox with plain file calls, and fails the test if any of them would land outside it.
type fakeExecutor struct {
	t *testing.T
	// seen is the sandbox's home as the executor found it, before writing anything.
	seen map[string]string
	got  []adapter.Op
}

func (f *fakeExecutor) Apply(_ context.Context, box *sandbox.Sandbox, ops []adapter.Op) error {
	f.seen = tree(f.t, box.Home())
	f.got = ops
	resolve := func(p string) string {
		var out string
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			out = filepath.Join(box.Home(), filepath.FromSlash(rest))
		} else {
			out = filepath.FromSlash(p)
		}
		root := filepath.Dir(box.Home())
		if rel, err := filepath.Rel(root, out); err != nil || !filepath.IsLocal(rel) {
			f.t.Fatalf("the executor was given %q, which lies outside the sandbox %s", p, root)
		}
		return out
	}
	for _, op := range ops {
		switch o := op.(type) {
		case adapter.WriteTree:
			dir := resolve(o.Dir)
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			for _, file := range o.Files {
				write(f.t, filepath.Join(dir, filepath.FromSlash(file.Path)), string(file.Data))
			}
		case adapter.WriteFile:
			write(f.t, resolve(o.Path), string(o.Data))
		case adapter.SetKey:
			p := resolve(o.File)
			old, err := os.ReadFile(p)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			write(f.t, p, string(old)+fmt.Sprintf("%s = %v\n", strings.Join(o.Path, "."), o.Value))
		case adapter.Exec:
			f.t.Errorf("the executor was given an Exec operation in a preview: %v", o.Argv)
		}
	}
	return nil
}

var skill = resource.Ref{Kind: resource.KindSkill, Code: "unit-testing"}

func TestPreviewCopiesOnlyTheDeclaredTargets(t *testing.T) {
	w := newWorld(t)
	tree1 := ".claude/skills/" + brand.Prefix + "unit-testing"
	w.write(".claude.json", "{\n  \"numStartups\": 12\n}\n")
	w.write(".claude/settings.json", "{\"model\": \"example\"}\n")
	w.write(".claude/skills/my-own/SKILL.md", "mine\n")
	w.write(tree1+"/SKILL.md", "version 1\n")
	w.write(tree1+"/old.md", "dropped in version 2\n")
	w.write(tree1+"/.credentials.json", "<占位：令牌>\n")
	w.write(".codex/config.toml", "model = \"example\"\n")
	w.write(".codex/auth.json", "{\"token\": \"<占位：令牌>\"}\n")
	w.write("notes.txt", "unrelated\n")
	realBefore := tree(t, w.home)

	ops := []adapter.Op{
		adapter.WriteTree{Resource: skill, Dir: "~/" + tree1, Files: []adapter.File{{Path: "SKILL.md", Data: []byte("version 2\n")}}},
		adapter.SetKey{Resource: skill, File: "~/.claude.json", Format: adapter.JSON, Path: []string{"mcpServers", brand.Prefix + "jira"}, Value: "x"},
		adapter.WriteFile{Resource: skill, Path: "~/.claude/commands/" + brand.Prefix + "review.md", Data: []byte("review\n")},
		adapter.Exec{Resource: skill, Argv: []string{"claude", "plugin", "install", "example"}},
	}
	exec := &fakeExecutor{t: t}
	res, err := sandbox.Preview(context.Background(), w.opts(), sandbox.Request{Ops: ops, Touches: []string{"~/.codex/auth.json"}}, exec)
	if err != nil {
		t.Fatal(err)
	}

	// Only the targets, and the directories leading to them, were there for the executor.
	wantSeen := map[string]string{
		".claude.json":      "{\n  \"numStartups\": 12\n}\n",
		".claude/":          "",
		".claude/skills/":   "",
		tree1 + "/":         "",
		tree1 + "/SKILL.md": "version 1\n",
		tree1 + "/old.md":   "dropped in version 2\n",
		".codex/":           "",
	}
	if !mapsEqual(exec.seen, wantSeen) {
		t.Errorf("the sandbox held:\n%s\nwant only:\n%s", show(exec.seen), show(wantSeen))
	}

	if len(exec.got) != 3 {
		t.Errorf("the executor got %d operations, want 3 (Exec taken out)", len(exec.got))
	}
	if len(res.NotRun) != 1 || res.NotRun[0].Argv[0] != "claude" {
		t.Errorf("NotRun = %v, want the Exec operation", res.NotRun)
	}
	wantWithheld := []string{"~/" + tree1 + "/.credentials.json", "~/.codex/auth.json"}
	slices.Sort(wantWithheld)
	if !slices.Equal(res.Withheld, wantWithheld) {
		t.Errorf("Withheld = %v, want %v", res.Withheld, wantWithheld)
	}

	type key struct {
		path   string
		action sandbox.Action
		kind   sandbox.Kind
	}
	var got []key
	for _, c := range res.Changes {
		got = append(got, key{c.Path, c.Action, c.Kind})
		wantReal := filepath.Join(w.home, filepath.FromSlash(strings.TrimPrefix(c.Path, "~/")))
		if c.Real != wantReal {
			t.Errorf("%s: Real = %q, want %q", c.Path, c.Real, wantReal)
		}
	}
	want := []key{
		{"~/.claude.json", sandbox.Modified, sandbox.File},
		{"~/.claude/commands", sandbox.Created, sandbox.Dir},
		{"~/.claude/commands/" + brand.Prefix + "review.md", sandbox.Created, sandbox.File},
		{"~/" + tree1 + "/SKILL.md", sandbox.Modified, sandbox.File},
		{"~/" + tree1 + "/old.md", sandbox.Removed, sandbox.File},
	}
	if !slices.Equal(got, want) {
		t.Errorf("changes:\n%v\nwant:\n%v", got, want)
	}
	for _, c := range res.Changes {
		if c.Path == "~/.claude.json" {
			if string(c.Before) != "{\n  \"numStartups\": 12\n}\n" || !strings.HasSuffix(string(c.After), "mcpServers."+brand.Prefix+"jira = x\n") {
				t.Errorf("~/.claude.json: Before %q, After %q", c.Before, c.After)
			}
		}
	}

	// The real home directory is exactly as it was, and the sandbox is gone.
	if realAfter := tree(t, w.home); !mapsEqual(realAfter, realBefore) {
		t.Errorf("the real home changed:\n%s\nwas:\n%s", show(realAfter), show(realBefore))
	}
	assertEmpty(t, w.tmp)
}

func TestCredentialFileDeclaredAsTargetIsNotCopied(t *testing.T) {
	w := newWorld(t)
	w.write(".codex/Auth.JSON", "{\"token\": \"<占位：令牌>\"}\n")
	ops := []adapter.Op{adapter.SetKey{Resource: skill, File: "~/.codex/Auth.JSON", Format: adapter.JSON, Path: []string{brand.Prefix + "x"}, Value: "y"}}
	exec := &fakeExecutor{t: t}
	res, err := sandbox.Preview(context.Background(), w.opts(), sandbox.Request{Ops: ops}, exec)
	if err != nil {
		t.Fatal(err)
	}
	for p, content := range exec.seen {
		if strings.Contains(content, "<占位：令牌>") {
			t.Errorf("the credential file was copied to %s", p)
		}
	}
	if !slices.Equal(res.Withheld, []string{"~/.codex/Auth.JSON"}) {
		t.Errorf("Withheld = %v", res.Withheld)
	}
	if len(res.Changes) != 1 || !res.Changes[0].Withheld {
		t.Errorf("changes = %+v, want one change marked Withheld", res.Changes)
	}
}

func TestTargetsOutsideTheHomeDirectory(t *testing.T) {
	w := newWorld(t)
	outside := t.TempDir() // a tool root moved out of the home directory, e.g. by OPENCODE_CONFIG_DIR
	config := filepath.Join(outside, "opencode", "opencode.jsonc")
	write(t, config, "{\n  // mine\n}\n")
	w.write(".claude.json", "{}\n")

	ops := []adapter.Op{
		adapter.SetKey{Resource: skill, File: filepath.ToSlash(config), Format: adapter.JSONC, Path: []string{"mcp", brand.Prefix + "jira"}, Value: "x"},
		// An absolute path inside the home directory is the same as "~/…".
		adapter.SetKey{Resource: skill, File: filepath.ToSlash(w.path(".claude.json")), Format: adapter.JSON, Path: []string{brand.Prefix + "x"}, Value: "y"},
	}
	exec := &fakeExecutor{t: t}
	res, err := sandbox.Preview(context.Background(), w.opts(), sandbox.Request{Ops: ops}, exec)
	if err != nil {
		t.Fatal(err)
	}
	if f := exec.got[1].(adapter.SetKey).File; f != "~/.claude.json" {
		t.Errorf("the executor got %q, want ~/.claude.json", f)
	}
	if f := exec.got[0].(adapter.SetKey).File; f == filepath.ToSlash(config) {
		t.Errorf("the executor got the real path %q outside the home directory", f)
	}
	var paths []string
	for _, c := range res.Changes {
		paths = append(paths, c.Path)
		if c.Path == filepath.ToSlash(config) {
			if c.Real != config || c.Action != sandbox.Modified || string(c.Before) != "{\n  // mine\n}\n" {
				t.Errorf("change outside home = %+v", c)
			}
		}
	}
	if !slices.Equal(paths, sorted(filepath.ToSlash(config), "~/.claude.json")) {
		t.Errorf("changed paths = %v", paths)
	}
	data, _ := os.ReadFile(config)
	if string(data) != "{\n  // mine\n}\n" {
		t.Errorf("the real file outside home changed: %q", data)
	}
}

func sorted(s ...string) []string { slices.Sort(s); return s }

func TestRefusedTargets(t *testing.T) {
	w := newWorld(t)
	for _, target := range []string{
		"relative/path",
		"~/../elsewhere",
		"~/a/../../elsewhere",
		"~",
		"~/",
		filepath.ToSlash(w.home),
		"~/" + brand.Home,
		"~/" + brand.Home + "/tmp/x",
		filepath.ToSlash(w.tmp),
		"~/a\\..\\..\\b",
	} {
		exec := &fakeExecutor{t: t}
		_, err := sandbox.Preview(context.Background(), w.opts(), sandbox.Request{Touches: []string{target}}, exec)
		if !errors.Is(err, sandbox.ErrTarget) {
			t.Errorf("target %q: err = %v, want ErrTarget", target, err)
		}
		if exec.seen != nil {
			t.Errorf("target %q: the executor ran", target)
		}
	}
	assertEmpty(t, w.tmp)
}

func TestUnreadableTargetIsAnError(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not stop this user from reading")
	}
	w := newWorld(t)
	w.write(".claude.json", "{}\n")
	if err := os.Chmod(w.path(".claude.json"), 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(w.path(".claude.json"), 0o600)
	exec := &fakeExecutor{t: t}
	_, err := sandbox.Preview(context.Background(), w.opts(), sandbox.Request{Touches: []string{"~/.claude.json"}}, exec)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		t.Errorf("err = %v, want a permission error (unreadable is not missing)", err)
	}
	if exec.seen != nil {
		t.Error("the executor ran")
	}
	assertEmpty(t, w.tmp)
}

func TestFailedApplyRemovesTheSandbox(t *testing.T) {
	w := newWorld(t)
	w.write(".claude.json", "{}\n")
	boom := errors.New("executor failed")
	var home string
	_, err := sandbox.Preview(context.Background(), w.opts(), sandbox.Request{Touches: []string{"~/.claude.json"}},
		sandbox.ApplierFunc(func(_ context.Context, box *sandbox.Sandbox, _ []adapter.Op) error {
			home = box.Home()
			return boom
		}))
	if !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(home); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the sandbox %s is still there (%v)", home, err)
	}
	assertEmpty(t, w.tmp)
}

func TestExecutorStateStaysInTheSandbox(t *testing.T) {
	w := newWorld(t)
	_, err := sandbox.Preview(context.Background(), w.opts(), sandbox.Request{},
		sandbox.ApplierFunc(func(_ context.Context, box *sandbox.Sandbox, _ []adapter.Op) error {
			// Backups and the intent log go to DataDir and are not part of the diff.
			write(t, filepath.Join(box.DataDir(), "backup", "claude.json"), "{}\n")
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	assertEmpty(t, w.tmp)
}

func TestLeftoversAreRemovedAtStart(t *testing.T) {
	w := newWorld(t)
	old := filepath.Join(w.tmp, "plan-1234")
	fresh := filepath.Join(w.tmp, "plan-5678")
	other := filepath.Join(w.tmp, "something-else")
	for _, d := range []string{old, fresh, other} {
		write(t, filepath.Join(d, "home", ".claude.json"), "{}\n")
	}
	long := time.Now().Add(-2 * sandbox.StaleAfter)
	for _, d := range []string{old, other} {
		if err := os.Chtimes(d, long, long); err != nil {
			t.Fatal(err)
		}
	}

	box, err := sandbox.New(w.opts())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the old leftover is still there (%v)", err)
	}
	for _, d := range []string{fresh, other} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s was removed: a fresh sandbox may belong to another run, and other names are not ours", d)
		}
	}
	root := filepath.Dir(box.Home())
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("the sandbox has mode %v, want 0700", info.Mode().Perm())
	}
	if err := box.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Close left %s behind", root)
	}
}

func TestSymlinkedToolDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need extra privileges on Windows")
	}
	w := newWorld(t)
	w.write("dotfiles/claude/settings.json", "{}\n")
	w.write("dotfiles/claude/skills/"+brand.Prefix+"a/SKILL.md", "a\n")
	if err := os.Symlink(w.path("dotfiles/claude/skills/"+brand.Prefix+"a/SKILL.md"), w.path("dotfiles/claude/skills/linked.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(w.home, "dotfiles", "claude"), w.path(".claude")); err != nil {
		t.Fatal(err)
	}
	realBefore := tree(t, w.home)
	ops := []adapter.Op{adapter.SetKey{Resource: skill, File: "~/.claude/settings.json", Format: adapter.JSON, Path: []string{brand.Prefix + "x"}, Value: "y"}}
	exec := &fakeExecutor{t: t}
	res, err := sandbox.Preview(context.Background(), w.opts(), sandbox.Request{Ops: ops, Touches: []string{"~/.claude/skills"}}, exec)
	if err != nil {
		t.Fatal(err)
	}
	if exec.seen[".claude/settings.json"] != "{}\n" || exec.seen[".claude/skills/linked.md"] != "a\n" {
		t.Errorf("the sandbox held %s", show(exec.seen))
	}
	if len(res.Changes) != 1 || res.Changes[0].Path != "~/.claude/settings.json" {
		t.Errorf("changes = %+v", res.Changes)
	}
	if realAfter := tree(t, w.home); !mapsEqual(realAfter, realBefore) {
		t.Errorf("the real home changed")
	}
}

func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("left behind in %s: %s", dir, e.Name())
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func show(m map[string]string) string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s %q\n", k, m[k])
	}
	return b.String()
}
