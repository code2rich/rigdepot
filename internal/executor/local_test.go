// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package executor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// agentHome is the directory of an agent: its instructions, one skill of its own, and a memory
// directory that belongs to the person's computer once it is there, seeded with a README.
func agentHome(version string) adapter.WriteTree {
	return adapter.WriteTree{
		Resource: resource.Ref{Kind: resource.KindAgent, Code: "backend-developer"},
		Dir:      "~/.claude/skills/" + brand.Prefix + "backend-developer",
		Files: []adapter.File{
			{Path: "SKILL.md", Data: []byte("---\nname: backend-developer\n---\n# Backend developer " + version + "\n")},
			{Path: "skills/api/SKILL.md", Data: []byte("# API " + version + "\n")},
			{Path: "memory/README.md", Data: []byte("Notes go here. " + version + "\n")},
		},
		Local: []string{"memory", "workspace"},
	}
}

// symlink makes a symbolic link where the system lets a test make one; Windows needs a
// privilege for it. It reports whether the link was made.
func symlink(t *testing.T, oldname, newname string) bool {
	t.Helper()
	if runtime.GOOS == "windows" {
		return false
	}
	if err := os.Symlink(oldname, newname); err != nil {
		t.Fatal(err)
	}
	return true
}

func agentPath(e *env, rel string) string {
	return filepath.Join(e.home, ".claude", "skills", brand.Prefix+"backend-developer", filepath.FromSlash(rel))
}

// What the person's tools write into a local directory is not a change of ours: the digest does
// not count it, so a sync neither reports it nor overwrites it.
func TestLocalDirectoriesDoNotCountTowardsTheDigest(t *testing.T) {
	e := newEnv(t)
	op := agentHome("v1")
	res := e.apply(t, op, Options{})
	want, err := Expect(op)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry.Digest != want || want != filesDigest(op.Files[:2]) {
		t.Fatalf("recorded %s, Expect %s; want the digest of the files outside memory/", res.Entry.Digest, want)
	}
	if got := strings.Join(res.Entry.Local, ","); got != "memory,workspace" {
		t.Errorf("the entry records the local directories %q", got)
	}
	writeFile(t, agentPath(e, "memory/2026-11-25.md"), "what I learnt today\n")
	writeFile(t, agentPath(e, "memory/README.md"), "rewritten by the agent\n")
	writeFile(t, agentPath(e, "workspace/draft.md"), "a draft\n")
	symlink(t, "draft.md", agentPath(e, "workspace/latest"))
	if got := e.ex.Observe(TargetOfEntry(res.Entry)); got.State != Present || got.Digest != res.Entry.Digest {
		t.Fatalf("after writing into the local directories Observe = %+v, want the recorded digest", got)
	}
	// Outside them a change still shows.
	writeFile(t, agentPath(e, "skills/api/notes.md"), "mine\n")
	if got := e.ex.Observe(TargetOfEntry(res.Entry)); got.Digest == res.Entry.Digest {
		t.Fatal("a file added outside the local directories does not change the digest")
	}
}

// An update replaces everything but the local directories, which keep what the person's
// computer put there, also where the new version seeds the same file.
func TestUpdatingKeepsWhatIsInTheLocalDirectories(t *testing.T) {
	e := newEnv(t)
	e.apply(t, agentHome("v1"), Options{})
	writeFile(t, agentPath(e, "memory/README.md"), "rewritten by the agent\n")
	writeFile(t, agentPath(e, "memory/deep/fact.md"), "a fact\n")
	linked := symlink(t, "deep/fact.md", agentPath(e, "memory/latest"))

	res := e.apply(t, agentHome("v2"), Options{})
	for rel, want := range map[string]string{
		"SKILL.md":            "Backend developer v2",
		"skills/api/SKILL.md": "API v2",
		"memory/README.md":    "rewritten by the agent",
		"memory/deep/fact.md": "a fact",
	} {
		got, err := os.ReadFile(agentPath(e, rel))
		if err != nil || !strings.Contains(string(got), want) {
			t.Errorf("%s: %q, %v; want %q", rel, got, err, want)
		}
	}
	if link, err := os.Readlink(agentPath(e, "memory/latest")); linked && (err != nil || link != "deep/fact.md") {
		t.Errorf("the link in memory/: %q, %v", link, err)
	}
	if got := e.ex.Observe(TargetOfEntry(res.Entry)); got.Digest != res.Entry.Digest {
		t.Errorf("after the update Observe = %+v, want %s", got, res.Entry.Digest)
	}
	e.assertNoIntents(t)
}

// A new directory gets the seeded content of its local directories.
func TestANewDirectoryGetsTheSeedOfItsLocalDirectories(t *testing.T) {
	e := newEnv(t)
	e.apply(t, agentHome("v1"), Options{})
	if got, err := os.ReadFile(agentPath(e, "memory/README.md")); err != nil || !strings.Contains(string(got), "v1") {
		t.Fatalf("memory/README.md: %q, %v", got, err)
	}
}

// Undoing keeps what is in the local directories: an agent's memory is the person's.
func TestRevertKeepsTheLocalDirectories(t *testing.T) {
	e := newEnv(t)
	initial := snapshot(t, e.home)
	res := e.apply(t, agentHome("v1"), Options{})
	writeFile(t, agentPath(e, "memory/fact.md"), "a fact\n")

	r, err := e.ex.Revert(ctx, res.Entry)
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != RemovedKept {
		t.Fatalf("Revert = %+v, want the memory kept", r)
	}
	if got, err := os.ReadFile(filepath.Join(e.data.Join(r.Kept), "memory", "fact.md")); err != nil || string(got) != "a fact\n" {
		t.Fatalf("kept memory/fact.md: %q, %v", got, err)
	}
	assertSame(t, initial, snapshot(t, e.home), "after undoing")

	// With nothing in them there is nothing to keep.
	op := agentHome("v1")
	op.Files = op.Files[:2]
	res = e.apply(t, op, Options{})
	if r, err := e.ex.Revert(ctx, res.Entry); err != nil || r.Outcome != Removed {
		t.Fatalf("Revert of an agent with empty local directories = %+v, %v; want removed", r, err)
	}
}

// Local directories are written the way files are: relative, clean, and not a file at the same
// time. Anything else is a defect of the adapter.
func TestBadLocalDirectoriesAreADefect(t *testing.T) {
	for _, local := range [][]string{{"../memory"}, {"/memory"}, {"memory/"}, {"."}, {"memory", "Memory"}, {"SKILL.md"}, {MarkerFile}} {
		e := newEnv(t)
		op := agentHome("v1")
		op.Local = local
		if _, err := e.ex.Apply(ctx, claude, op, e.ex.Observe(TargetOf(op)), Options{}); err == nil || !strings.Contains(err.Error(), "defect") {
			t.Errorf("Local %q: %v, want a defect", local, err)
		}
	}
}

// A manifest entry written before local directories existed reads as a tree without them.
func TestAnEntryWithoutLocalDirectories(t *testing.T) {
	var en manifest.Entry
	if err := en.UnmarshalJSON([]byte(`{"id":"x","tool":"claude-code","resource":"ab","op":"tree","path":"~/.claude/skills/` + brand.Prefix + `ab","digest":"sha256:` + strings.Repeat("0", 64) + `","at":"2026-11-25T00:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}
	if TargetOfEntry(en).Local != nil {
		t.Fatalf("local directories out of nothing: %v", TargetOfEntry(en).Local)
	}
}
