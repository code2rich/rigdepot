// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/manifest"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

var (
	ctx      = context.Background()
	fixedNow = func() time.Time { return time.Date(2026, 11, 24, 10, 2, 11, 0, time.UTC) }
	claude   = Tool{ID: "claude-code", Roots: map[string]string{"config": "~/.claude", "skills": "~/.claude/skills"}}
	skillRef = resource.Ref{Kind: resource.KindSkill, Code: "unit-testing"}
	skillDir = "~/.claude/skills/" + brand.Prefix + "unit-testing"
)

// env is a home directory as a person has it, with the local data directory inside it, the
// manifest locked and an executor on top.
type env struct {
	home   string
	data   home.Dir
	store  *manifest.Store
	locked *manifest.Locked
	ex     *Executor
}

func newEnv(t *testing.T) *env {
	t.Helper()
	h := t.TempDir()
	// The person already uses the tool: a settings file of their own, and a skill of their own.
	writeFile(t, filepath.Join(h, ".claude", "settings.json"), "{\n  // mine\n  \"theme\": \"dark\",\n}\n")
	return openEnv(t, h)
}

func openEnv(t *testing.T, h string) *env {
	t.Helper()
	data, err := home.At(filepath.Join(h, brand.Home))
	if err != nil {
		t.Fatal(err)
	}
	store := manifest.Open(data)
	locked, err := store.Lock(ctx, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ex, err := New(Roots{Home: h}, State{Data: data, Journal: locked, Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{home: h, data: data, store: store, locked: locked, ex: ex}
	t.Cleanup(e.close)
	return e
}

func (e *env) close() {
	if e.ex != nil {
		e.ex.Close()
		e.ex = nil
	}
	if e.locked != nil {
		e.locked.Release()
		e.locked = nil
	}
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skill(version string) adapter.WriteTree {
	return adapter.WriteTree{
		Resource: skillRef,
		Dir:      skillDir,
		Files: []adapter.File{
			{Path: "SKILL.md", Data: []byte("---\nname: unit-testing\n---\n# Unit testing " + version + "\n")},
			{Path: "scripts/run.sh", Data: []byte("#!/bin/sh\necho " + version + "\n"), Exec: true},
			{Path: "reference/checklist.md", Data: []byte("- " + version + "\n")},
		},
	}
}

// snapshot is the home directory as a map from path to content ("dir" for a directory),
// leaving out the local data directory.
func snapshot(t *testing.T, h string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(h, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(h, p)
		rel = filepath.ToSlash(rel)
		if rel == brand.Home {
			return filepath.SkipDir
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			out[rel] = "dir"
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func diffSnapshots(a, b map[string]string) string {
	var keys []string
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		va, ina := a[k]
		vb, inb := b[k]
		switch {
		case !ina:
			out = append(out, "+ "+k)
		case !inb:
			out = append(out, "- "+k)
		case va != vb:
			out = append(out, "~ "+k)
		}
	}
	return strings.Join(out, "\n")
}

func assertSame(t *testing.T, want, got map[string]string, what string) {
	t.Helper()
	if d := diffSnapshots(want, got); d != "" {
		t.Fatalf("%s; the home directory differs:\n%s", what, d)
	}
}

func (e *env) apply(t *testing.T, op adapter.Op, opt Options) Applied {
	t.Helper()
	res, err := e.ex.Apply(ctx, claude, op, e.ex.Observe(TargetOf(op)), opt)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return res
}

func (e *env) entries(t *testing.T) []manifest.Entry {
	t.Helper()
	m, err := e.store.Read()
	if err != nil {
		t.Fatal(err)
	}
	return m.Entries
}

func (e *env) assertNoIntents(t *testing.T) {
	t.Helper()
	pending, err := e.locked.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("%d intents are left in the log", len(pending))
	}
}

func TestTheDigestIsTheOneTheShellCommandPrints(t *testing.T) {
	// Computed with: find . -type f | LC_ALL=C sort | sed 's|^\./||' | xargs shasum -a 256 | shasum -a 256
	files := []adapter.File{
		{Path: "SKILL.md", Data: []byte("# Unit testing\n")},
		{Path: "scripts/run.sh", Data: []byte("echo hi\n")},
		{Path: "B/a.txt", Data: []byte("b\n")},
		{Path: "a.md", Data: []byte("lower\n")},
	}
	const want = "sha256:da67282d459ac6a416926f9904548c768812b11f389e78ad628e97cd14bb06c4"
	if got := filesDigest(files); got != want {
		t.Fatalf("filesDigest = %s, want %s", got, want)
	}
	e := newEnv(t)
	op := adapter.WriteTree{Resource: skillRef, Dir: skillDir, Files: files}
	res := e.apply(t, op, Options{})
	if res.Entry.Digest != want {
		t.Errorf("recorded digest %s, want %s", res.Entry.Digest, want)
	}
	if o := e.ex.Observe(TargetOf(op)); o.State != Present || o.Digest != want {
		t.Errorf("Observe = %+v; the marker file must not count", o)
	}
}

func TestApplyWritesTheTreeAndRecordsIt(t *testing.T) {
	e := newEnv(t)
	op := skill("v1")
	res := e.apply(t, op, Options{})

	dir := filepath.Join(e.home, filepath.FromSlash(strings.TrimPrefix(skillDir, "~/")))
	for _, f := range op.Files {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil || string(got) != string(f.Data) {
			t.Errorf("%s: %q, %v", f.Path, got, err)
		}
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, "scripts", "run.sh"))
		if fi.Mode().Perm()&0o100 == 0 {
			t.Errorf("run.sh is %v; it asked for the executable bit", fi.Mode())
		}
	}
	marker, err := os.ReadFile(filepath.Join(dir, MarkerFile))
	if err != nil || !strings.Contains(string(marker), brand.ManagedMarker) {
		t.Errorf("marker file: %q, %v", marker, err)
	}

	want := manifest.Entry{
		ID: "claude-code:tree:" + skillDir, Tool: "claude-code", Resource: "unit-testing",
		Op: manifest.OpTree, Path: skillDir, Digest: filesDigest(op.Files),
		Created: []string{"~/.claude/skills"}, At: fixedNow(),
	}
	if fmt.Sprint(res.Entry) != fmt.Sprint(want) {
		t.Errorf("entry\n got %+v\nwant %+v", res.Entry, want)
	}
	if got := e.entries(t); len(got) != 1 || fmt.Sprint(got[0]) != fmt.Sprint(want) {
		t.Errorf("manifest holds %+v", got)
	}
	e.assertNoIntents(t)
	if o := e.ex.Observe(TargetOf(op)); o.State != Present || o.Digest != want.Digest {
		t.Errorf("Observe after Apply = %+v", o)
	}
	entries, _ := os.ReadDir(filepath.Dir(dir))
	if len(entries) != 1 {
		t.Errorf("the skills directory holds %d entries; temporary directories were left behind", len(entries))
	}
}

func TestTargetsThatAreAnAdapterDefectAreRefused(t *testing.T) {
	outside := filepath.ToSlash(t.TempDir())
	good := skill("v1").Files
	cases := []struct {
		name string
		op   adapter.Op
	}{
		{"no prefix", adapter.WriteTree{Resource: skillRef, Dir: "~/.claude/skills/unit-testing", Files: good}},
		{"only the prefix", adapter.WriteTree{Resource: skillRef, Dir: "~/.claude/skills/" + brand.Prefix, Files: good}},
		{"prefix on the parent only", adapter.WriteFile{Resource: skillRef, Path: "~/.claude/" + brand.Prefix + "x/settings.json", Data: []byte("x")}},
		{"another tool's root", adapter.WriteTree{Resource: skillRef, Dir: "~/.codex/skills/" + brand.Prefix + "x", Files: good}},
		{"the home directory itself", adapter.WriteFile{Resource: skillRef, Path: "~/" + brand.Prefix + "x", Data: []byte("x")}},
		{"dot-dot out of the root", adapter.WriteTree{Resource: skillRef, Dir: "~/.claude/skills/../../" + brand.Prefix + "x", Files: good}},
		{"dot element", adapter.WriteTree{Resource: skillRef, Dir: "~/.claude/./skills/" + brand.Prefix + "x", Files: good}},
		{"backslash", adapter.WriteTree{Resource: skillRef, Dir: `~/.claude\skills\` + brand.Prefix + "x", Files: good}},
		{"absolute outside every root", adapter.WriteTree{Resource: skillRef, Dir: outside + "/" + brand.Prefix + "x", Files: good}},
		{"relative", adapter.WriteTree{Resource: skillRef, Dir: ".claude/skills/" + brand.Prefix + "x", Files: good}},
		{"file leaving the tree", adapter.WriteTree{Resource: skillRef, Dir: skillDir, Files: []adapter.File{{Path: "../escape.md", Data: []byte("x")}}}},
		{"absolute file in the tree", adapter.WriteTree{Resource: skillRef, Dir: skillDir, Files: []adapter.File{{Path: "/etc/x", Data: []byte("x")}}}},
		{"the marker as a file", adapter.WriteTree{Resource: skillRef, Dir: skillDir, Files: []adapter.File{{Path: MarkerFile, Data: []byte("{}")}}}},
		{"two files differing in case", adapter.WriteTree{Resource: skillRef, Dir: skillDir, Files: []adapter.File{{Path: "a.md"}, {Path: "A.md"}}}},
		{"a file and a directory of one name", adapter.WriteTree{Resource: skillRef, Dir: skillDir, Files: []adapter.File{{Path: "a"}, {Path: "a/b"}}}},
		{"no resource", adapter.WriteTree{Dir: skillDir, Files: good}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			before := snapshot(t, e.home)
			_, err := e.ex.Apply(ctx, claude, c.op, Observed{State: Absent}, Options{})
			var de *DefectError
			if !errors.Is(err, ErrDefect) || !errors.As(err, &de) {
				t.Fatalf("Apply = %v, want an adapter defect", err)
			}
			assertSame(t, before, snapshot(t, e.home), "a refused operation changed something")
			e.assertNoIntents(t)
			if n := len(e.entries(t)); n != 0 {
				t.Fatalf("a refused operation was recorded (%d entries)", n)
			}
		})
	}
	t.Run("outside dir untouched", func(t *testing.T) {
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Fatalf("something was written outside every root: %v", entries)
		}
	})
}

func TestASymbolicLinkDoesNotLeadOutOfTheHomeDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symbolic links needs privileges on Windows")
	}
	e := newEnv(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(e.home, ".claude", "skills")); err != nil {
		t.Fatal(err)
	}
	_, err := e.ex.Apply(ctx, claude, skill("v1"), Observed{State: Absent}, Options{})
	if err == nil {
		t.Fatal("wrote through a symbolic link that points out of the home directory")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("files appeared outside the home directory: %v", entries)
	}
	e.assertNoIntents(t)
}

func TestApplyRefusesATargetThatChangedSinceThePlan(t *testing.T) {
	e := newEnv(t)
	op := skill("v1")
	planned := e.ex.Observe(TargetOf(op)) // absent
	// The person creates a directory of that name between plan and write.
	writeFile(t, filepath.Join(e.home, ".claude", "skills", brand.Prefix+"unit-testing", "SKILL.md"), "mine\n")
	before := snapshot(t, e.home)
	if _, err := e.ex.Apply(ctx, claude, op, planned, Options{}); !errors.Is(err, ErrChanged) {
		t.Fatalf("Apply = %v, want ErrChanged", err)
	}
	assertSame(t, before, snapshot(t, e.home), "a changed target was written")
	e.assertNoIntents(t)

	if _, err := e.ex.Apply(ctx, claude, op, Observed{}, Options{}); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("Apply with a plan that could not read the target = %v, want ErrUnreadable", err)
	}
}

func TestReplacingADirectoryThatFailsHalfwayLeavesItAsItWas(t *testing.T) {
	for _, failAt := range []string{stepStaged, stepMovedOld, stepMovedNew} {
		t.Run(failAt, func(t *testing.T) {
			e := newEnv(t)
			first := e.apply(t, skill("v1"), Options{})
			before := snapshot(t, e.home)

			calls := 0
			e.ex.hook = func(step string) error {
				if step != failAt {
					return nil
				}
				calls++
				if step == stepStaged && calls < 2 {
					return nil // fail on the second file, with one already written
				}
				return fmt.Errorf("injected failure at %s", step)
			}
			op := skill("v2")
			_, err := e.ex.Apply(ctx, claude, op, e.ex.Observe(TargetOf(op)), Options{})
			if err == nil || !strings.Contains(err.Error(), "injected failure") {
				t.Fatalf("Apply = %v, want the injected failure", err)
			}
			assertSame(t, before, snapshot(t, e.home), "the directory is not as it was")
			e.assertNoIntents(t)
			if got := e.entries(t); len(got) != 1 || got[0].Digest != first.Entry.Digest {
				t.Fatalf("manifest after the failure: %+v", got)
			}
		})
	}
}

func TestReplacingADirectoryWithNewContent(t *testing.T) {
	e := newEnv(t)
	e.apply(t, skill("v1"), Options{})
	op := skill("v2")
	op.Files = op.Files[:1] // v2 dropped two files
	res := e.apply(t, op, Options{})
	snap := snapshot(t, e.home)
	base := strings.TrimPrefix(skillDir, "~/")
	if _, ok := snap[base+"/scripts/run.sh"]; ok {
		t.Error("a file of the old version survived the replacement")
	}
	if !strings.Contains(snap[base+"/SKILL.md"], "v2") {
		t.Error("the new content is not in place")
	}
	if res.Kept != "" {
		t.Errorf("kept %s without being asked", res.Kept)
	}
	if got := e.entries(t); len(got) != 1 || got[0].Digest != filesDigest(op.Files) {
		t.Fatalf("manifest: %+v", got)
	}
}

func TestOverwritingWithKeepCopiesTheOldContentFirst(t *testing.T) {
	e := newEnv(t)
	e.apply(t, skill("v1"), Options{})
	changed := filepath.Join(e.home, ".claude", "skills", brand.Prefix+"unit-testing", "SKILL.md")
	writeFile(t, changed, "changed by hand\n")
	res := e.apply(t, skill("v2"), Options{Keep: true})
	if res.Kept == "" {
		t.Fatal("nothing was kept")
	}
	got, err := os.ReadFile(filepath.Join(e.data.Join(res.Kept), "SKILL.md"))
	if err != nil || string(got) != "changed by hand\n" {
		t.Fatalf("kept SKILL.md: %q, %v", got, err)
	}
}

func TestRevertRestoresTheHomeDirectory(t *testing.T) {
	e := newEnv(t)
	initial := snapshot(t, e.home)
	res := e.apply(t, skill("v1"), Options{})
	file := e.apply(t, adapter.WriteFile{Resource: skillRef, Path: "~/.claude/commands/" + brand.Prefix + "review.md", Data: []byte("# Review\n")}, Options{})

	for _, en := range []manifest.Entry{file.Entry, res.Entry} { // the reverse of the order written
		r, err := e.ex.Revert(ctx, en)
		if err != nil {
			t.Fatalf("Revert %s: %v", en.ID, err)
		}
		if r.Outcome != Removed || r.Kept != "" {
			t.Errorf("Revert %s = %+v, want removed and nothing kept", en.ID, r)
		}
	}
	assertSame(t, initial, snapshot(t, e.home), "after undoing everything")
	if n := len(e.entries(t)); n != 0 {
		t.Fatalf("%d entries left", n)
	}
	e.assertNoIntents(t)
}

func TestRevertKeepsAChangedDirectoryBeforeRemovingIt(t *testing.T) {
	e := newEnv(t)
	res := e.apply(t, skill("v1"), Options{})
	dir := filepath.Join(e.home, ".claude", "skills", brand.Prefix+"unit-testing")
	writeFile(t, filepath.Join(dir, "notes.md"), "my own notes\n")

	r, err := e.ex.Revert(ctx, res.Entry)
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != RemovedKept || !strings.HasPrefix(r.Kept, home.KeptDir+"/") {
		t.Fatalf("Revert = %+v, want the changed directory kept", r)
	}
	kept := e.data.Join(r.Kept)
	if got, err := os.ReadFile(filepath.Join(kept, "notes.md")); err != nil || string(got) != "my own notes\n" {
		t.Fatalf("the person's notes were not kept: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(kept, "SKILL.md")); err != nil || !strings.Contains(string(got), "v1") {
		t.Fatalf("SKILL.md was not kept: %q, %v", got, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the directory is still there: %v", err)
	}
	if n := len(e.entries(t)); n != 0 {
		t.Fatalf("%d entries left", n)
	}
}

func TestRevertOfATargetThatIsGoneOnlyDropsTheEntry(t *testing.T) {
	e := newEnv(t)
	res := e.apply(t, skill("v1"), Options{})
	if err := os.RemoveAll(filepath.Join(e.home, ".claude", "skills", brand.Prefix+"unit-testing")); err != nil {
		t.Fatal(err)
	}
	r, err := e.ex.Revert(ctx, res.Entry)
	if err != nil || r.Outcome != AlreadyGone {
		t.Fatalf("Revert = %+v, %v", r, err)
	}
	if n := len(e.entries(t)); n != 0 {
		t.Fatalf("%d entries left", n)
	}
}

func TestRevertLeavesParentsThatAreNoLongerEmpty(t *testing.T) {
	e := newEnv(t)
	res := e.apply(t, skill("v1"), Options{})
	writeFile(t, filepath.Join(e.home, ".claude", "skills", "my-own", "SKILL.md"), "mine\n")
	if _, err := e.ex.Revert(ctx, res.Entry); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.home, ".claude", "skills", "my-own", "SKILL.md")); err != nil {
		t.Fatalf("the person's own skill was touched: %v", err)
	}
}

func TestWriteFileReplacesAndReverts(t *testing.T) {
	e := newEnv(t)
	initial := snapshot(t, e.home)
	path := "~/.claude/commands/" + brand.Prefix + "review.md"
	first := e.apply(t, adapter.WriteFile{Resource: skillRef, Path: path, Data: []byte("v1\n")}, Options{})
	second := e.apply(t, adapter.WriteFile{Resource: skillRef, Path: path, Data: []byte("v2\n")}, Options{})
	if first.Entry.ID != second.Entry.ID || second.Entry.Digest != fileDigest([]byte("v2\n")) {
		t.Fatalf("entries %+v and %+v", first.Entry, second.Entry)
	}
	got, _ := os.ReadFile(filepath.Join(e.home, ".claude", "commands", brand.Prefix+"review.md"))
	if string(got) != "v2\n" {
		t.Fatalf("file holds %q", got)
	}
	if n := len(e.entries(t)); n != 1 {
		t.Fatalf("%d entries, want the one entry updated in place", n)
	}
	if _, err := e.ex.Revert(ctx, second.Entry); err != nil {
		t.Fatal(err)
	}
	assertSame(t, initial, snapshot(t, e.home), "after undoing the file")
}

func TestWriteFileOverADirectoryMovesItAsideAndFailsBack(t *testing.T) {
	e := newEnv(t)
	path := "~/.claude/commands/" + brand.Prefix + "review.md"
	writeFile(t, filepath.Join(e.home, ".claude", "commands", brand.Prefix+"review.md", "inside"), "x")
	before := snapshot(t, e.home)
	op := adapter.WriteFile{Resource: skillRef, Path: path, Data: []byte("v1\n")}
	pre := e.ex.Observe(TargetOf(op))
	if pre.State != Present || !strings.HasPrefix(pre.Digest, "other:") {
		t.Fatalf("Observe of a directory at a file target = %+v", pre)
	}
	e.ex.hook = func(step string) error {
		if step == stepMovedNew {
			return errors.New("injected failure")
		}
		return nil
	}
	if _, err := e.ex.Apply(ctx, claude, op, pre, Options{}); err == nil {
		t.Fatal("the injected failure was not reported")
	}
	assertSame(t, before, snapshot(t, e.home), "a failed replacement of a directory by a file")
	e.ex.hook = nil
	e.apply(t, op, Options{})
	if got, _ := os.ReadFile(filepath.Join(e.home, ".claude", "commands", brand.Prefix+"review.md")); string(got) != "v1\n" {
		t.Fatalf("file holds %q", got)
	}
}

func TestExecHasAPlaceButIsNotImplemented(t *testing.T) {
	e := newEnv(t)
	before := snapshot(t, e.home)
	op := adapter.Exec{Resource: skillRef, Argv: []string{"claude", "plugin", "install", "x"}, Undo: []string{"claude", "plugin", "uninstall", "x"}}
	if _, err := e.ex.Apply(ctx, claude, op, Observed{State: Absent}, Options{}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Apply %T = %v, want ErrNotImplemented", op, err)
	}
	en := manifest.Entry{ID: "x", Tool: "claude-code", Resource: "x", Op: manifest.OpExec, Undo: op.Undo}
	if _, err := e.ex.Revert(ctx, en); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Revert of an exec entry = %v, want ErrNotImplemented", err)
	}
	assertSame(t, before, snapshot(t, e.home), "an operation that is not implemented changed something")
	e.assertNoIntents(t)
}

func TestAnUnreadableTargetIsUnknown(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not stop this user")
	}
	e := newEnv(t)
	res := e.apply(t, skill("v1"), Options{})
	sub := filepath.Join(e.home, ".claude", "skills", brand.Prefix+"unit-testing", "reference")
	if err := os.Chmod(sub, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sub, 0o755) })
	if o := e.ex.Observe(TargetOfEntry(res.Entry)); o.State != Unknown || o.Err == nil {
		t.Fatalf("Observe = %+v, want unknown", o)
	}
	if _, err := e.ex.Revert(ctx, res.Entry); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("Revert = %v, want ErrUnreadable", err)
	}
	if n := len(e.entries(t)); n != 1 {
		t.Fatal("the entry of an unreadable target was dropped")
	}
}

func TestAPreviewWithoutAJournalRecordsNothing(t *testing.T) {
	h := t.TempDir()
	ex, err := New(Roots{Home: h}, State{Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	res, err := ex.Apply(ctx, claude, skill("v1"), Observed{State: Absent}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry.Digest != filesDigest(skill("v1").Files) {
		t.Errorf("entry %+v", res.Entry)
	}
	if _, err := os.Stat(filepath.Join(h, brand.Home)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a preview created the local data directory: %v", err)
	}
}

func TestAnExtraRootOutsideTheHomeDirectory(t *testing.T) {
	h := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "claude-config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := home.At(filepath.Join(h, brand.Home))
	ex, err := New(Roots{Home: h, Extra: []string{cfg}}, State{Data: data, Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	root := filepath.ToSlash(cfg)
	tool := Tool{ID: "claude-code", Roots: map[string]string{"config": root}}
	op := adapter.WriteTree{Resource: skillRef, Dir: root + "/skills/" + brand.Prefix + "unit-testing", Files: skill("v1").Files}
	res, err := ex.Apply(ctx, tool, op, Observed{State: Absent}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry.Path != op.Dir || len(res.Entry.Created) != 1 || res.Entry.Created[0] != root+"/skills" {
		t.Fatalf("entry %+v", res.Entry)
	}
	if _, err := ex.Revert(ctx, res.Entry); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(cfg); len(entries) != 0 {
		t.Fatalf("left behind in the extra root: %v", entries)
	}
}
