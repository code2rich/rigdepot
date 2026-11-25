// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package executor

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/repoformat"
)

// dirKind is the least a directory-shaped kind can be: skills/<code>/, anything goes.
type dirKind struct{}

func (dirKind) ID() string                                   { return resource.KindSkill }
func (dirKind) Layout() kind.Layout                          { return kind.Layout{Dir: "skills"} }
func (dirKind) Schema() []byte                               { return nil }
func (dirKind) Forms() []kind.Form                           { return nil }
func (dirKind) Parse(resource.Ref, fs.FS) (kind.Spec, error) { return nil, nil }

// The digest a repository records for a resource, the digest the executor expects for the tree
// written from it, the one it records, and the one it observes afterwards are one value. Drift
// detection compares them with each other; if they were computed two ways, every skill would
// look changed.
func TestTheDigestIsTheOneTheRepositoryRecords(t *testing.T) {
	files := map[string]string{
		"SKILL.md":                "---\nname: unit-testing\n---\n# Unit testing\n",
		"meta.yaml":               "name: Unit testing\n",
		"scripts/run.sh":          "#!/bin/sh\necho hi\n",
		"a-b":                     "sorted by the whole path: a-b before a/b\n",
		"a/b":                     "inside a directory\n",
		"reference/Checklist.md":  "- one\n",
		"reference/checklist.txt": "- two\n",
	}
	repo := fstest.MapFS{brand.RepoConfig: {Data: []byte("schema: 1\n")}}
	var tree []adapter.File
	for p, data := range files {
		repo["skills/unit-testing/"+p] = &fstest.MapFile{Data: []byte(data)}
		tree = append(tree, adapter.File{Path: p, Data: []byte(data)})
	}
	kinds := kind.NewRegistry()
	kinds.Register(dirKind{})
	read, err := repoformat.Read(repo, repoformat.Options{Kinds: kinds})
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Resources) != 1 {
		t.Fatalf("read %d resources, failed %v", len(read.Resources), read.Failed)
	}
	want := string(read.Resources[0].Digest)

	op := adapter.WriteTree{Resource: skillRef, Dir: skillDir, Files: tree}
	if got, err := Expect(op); err != nil || got != want {
		t.Fatalf("Expect = %s, %v; the repository recorded %s", got, err, want)
	}
	e := newEnv(t)
	res := e.apply(t, op, Options{})
	if res.Entry.Digest != want {
		t.Errorf("the manifest recorded %s, the repository %s", res.Entry.Digest, want)
	}
	if o := e.ex.Observe(TargetOf(op)); o.State != Present || o.Digest != want {
		t.Errorf("Observe after writing = %+v, want %s", o, want)
	}
}

// A directory emptied by hand, or one with a link put into it, is present and is not what was
// written.
func TestTheDigestOfAnEmptiedOrLinkedTree(t *testing.T) {
	e := newEnv(t)
	op := skill("v1")
	res := e.apply(t, op, Options{})
	dir := filepath.Join(e.home, filepath.FromSlash(skillDir[2:]))

	if err := os.Symlink("SKILL.md", filepath.Join(dir, "link.md")); err == nil {
		o := e.ex.Observe(TargetOf(op))
		if o.State != Present || o.Digest == res.Entry.Digest || o.Digest[:6] != "other:" {
			t.Errorf("a tree with a symbolic link in it observes as %+v", o)
		}
		os.Remove(filepath.Join(dir, "link.md"))
	}

	for _, f := range op.Files {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(f.Path))); err != nil {
			t.Fatal(err)
		}
	}
	o := e.ex.Observe(TargetOf(op))
	if o.State != Present || o.Digest != emptyDigest || o.Digest == res.Entry.Digest {
		t.Errorf("an emptied tree observes as %+v", o)
	}
}

func TestExpectOfAKeyIsCanonicalJSON(t *testing.T) {
	a := adapter.SetKey{Value: map[string]any{"type": "http", "url": "https://mcp.example.com/a?b=1&c=<2>", "args": []any{"x", 1, true}}}
	b := adapter.SetKey{Value: map[string]any{"url": "https://mcp.example.com/a?b=1&c=<2>", "args": []any{"x", 1, true}, "type": "http"}}
	da, err := Expect(a)
	if err != nil {
		t.Fatal(err)
	}
	db, _ := Expect(b)
	want := fileDigest([]byte(`{"args":["x",1,true],"type":"http","url":"https://mcp.example.com/a?b=1&c=<2>"}`))
	if da != want || db != want {
		t.Errorf("Expect = %s and %s, want %s", da, db, want)
	}
	if _, err := Expect(adapter.Exec{Argv: []string{"x"}}); err == nil {
		t.Error("an Exec has a digest")
	}
}
