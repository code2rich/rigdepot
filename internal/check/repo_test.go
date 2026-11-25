// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package check

import (
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/repoformat"
)

// The real kinds live in builtin/kinds, which the core must not import. These do the least a
// kind does.

type dirKind struct{}

func (dirKind) ID() string          { return resource.KindSkill }
func (dirKind) Layout() kind.Layout { return kind.Layout{Dir: "skills"} }
func (dirKind) Schema() []byte      { return nil }
func (dirKind) Forms() []kind.Form  { return nil }
func (dirKind) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	data, err := fs.ReadFile(files, "SKILL.md")
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(data), "name: "+ref.Code+"\n") {
		return nil, errors.New("the name in SKILL.md is not " + ref.Code)
	}
	return ref, nil
}

type fileKind struct{}

func (fileKind) ID() string                                         { return resource.KindMCP }
func (fileKind) Layout() kind.Layout                                { return kind.Layout{Dir: "mcp", File: true, Ext: ".yaml"} }
func (fileKind) Schema() []byte                                     { return []byte(`{"type":"object","required":["meta"]}`) }
func (fileKind) Forms() []kind.Form                                 { return nil }
func (fileKind) Parse(ref resource.Ref, _ fs.FS) (kind.Spec, error) { return ref, nil }

func readRepo(t *testing.T, files fstest.MapFS) *repoformat.Repo {
	t.Helper()
	kinds := kind.NewRegistry()
	kinds.Register(dirKind{})
	kinds.Register(fileKind{})
	repo, err := repoformat.Read(files, repoformat.Options{Kinds: kinds})
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func file(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }

var repoFiles = fstest.MapFS{
	"rigdepot.yaml":                    file("schema: 1\ndefaults:\n  scope: org\n"),
	"skills/unit-testing/SKILL.md":     file("---\nname: unit-testing\n---\n"),
	"skills/unit-testing/meta.yaml":    file("name: Unit testing\n"),
	"skills/unit-testing/refs/a.md":    file("a\n"),
	"skills/wrong-name/SKILL.md":       file("---\nname: other\n---\n"),
	"skills/wrong-name/meta.yaml":      file("name: Wrong\n"),
	"skills/no-meta/SKILL.md":          file("---\nname: no-meta\n---\n"),
	"mcp/jira.yaml":                    file("meta:\n  name: Jira\n"),
	"mcp/broken.yaml":                  file("meta:\n  name: Broken\n  stages: 3\n"),
	"skills/unit-testing/.hidden/x.md": file("x\n"),
}

func TestFromRepoGivesTheEngineEveryFileAndTheEffectiveMetadata(t *testing.T) {
	located, err := FromRepo(readRepo(t, repoFiles))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, l := range located {
		paths = append(paths, l.Path)
	}
	if want := []string{"mcp/jira.yaml", "skills/unit-testing"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("resources %v, want %v", paths, want)
	}
	skill := located[1]
	if skill.Meta.Scope != "org" {
		t.Errorf("scope %q: the repository's default is not filled in", skill.Meta.Scope)
	}
	var names []string
	for p := range skill.Files {
		names = append(names, p)
	}
	for _, want := range []string{"SKILL.md", "meta.yaml", "refs/a.md", ".hidden/x.md"} {
		if _, ok := skill.Files[want]; !ok {
			t.Errorf("the engine does not get %s; it gets %v", want, names)
		}
	}
	if string(located[0].Files["jira.yaml"]) != "meta:\n  name: Jira\n" {
		t.Errorf("single-file resource: %v", located[0].Files)
	}
}

// What the reader refuses comes back as must-fix findings of the structure rule, with files
// relative to the resource, as every finding's are.
func TestRefusedResourcesAreStructureFindings(t *testing.T) {
	repo := readRepo(t, repoFiles)
	byPath := map[string]Result{}
	for _, f := range repo.Failed {
		byPath[f.Path] = Refused(f, 3)
	}
	if len(byPath) != 3 {
		t.Fatalf("refused: %v", repo.Failed)
	}
	for p, r := range byPath {
		if r.Passed() || r.RulesetVersion != 3 || len(r.Findings) == 0 {
			t.Errorf("%s: %+v", p, r)
		}
		for _, f := range r.Findings {
			if f.Rule != StructureRule || f.Severity != extcheck.MustFix || !strings.HasPrefix(f.Message, "check.structure.repo.") {
				t.Errorf("%s: %+v", p, f)
			}
			if f.Params["detail"] == "" {
				t.Errorf("%s: no detail: %+v", p, f)
			}
		}
	}
	if f := byPath["skills/no-meta"].Findings[0]; f.Message != msg.CheckStructureRepoMissingMeta || f.File != "meta.yaml" {
		t.Errorf("no meta.yaml: %+v", f)
	}
	if f := byPath["skills/wrong-name"].Findings[0]; f.Message != msg.CheckStructureRepoParse || f.File != "" || f.Line != 0 {
		t.Errorf("refused by the kind: %+v", f)
	}
	f := byPath["mcp/broken.yaml"].Findings[0]
	if f.Message != msg.CheckStructureRepoSchema || f.File != "broken.yaml" || f.Line != 3 || !strings.Contains(f.Params["detail"].(string), "stages") {
		t.Errorf("schema: %+v", f)
	}
}

func TestIssueFinding(t *testing.T) {
	cases := []struct {
		name  string
		is    repoformat.Issue
		res   string
		want  extcheck.Finding
		extra map[string]any
	}{
		{
			"a file inside a directory resource",
			repoformat.Issue{File: "skills/a/scripts/x", Code: repoformat.CodeUnsafePath, Field: "link", Message: "is a symbolic link"},
			"skills/a",
			extcheck.Finding{File: "scripts/x", Message: msg.CheckStructureRepoUnsafePath},
			map[string]any{"rule": "link", "detail": "is a symbolic link"},
		},
		{
			"the resource as a whole",
			repoformat.Issue{File: "skills/a", Code: repoformat.CodeInvalidCode, Message: "bad"},
			"skills/a",
			extcheck.Finding{Message: msg.CheckStructureRepoInvalidCode},
			map[string]any{"detail": "bad"},
		},
		{
			"a field of meta.yaml",
			repoformat.Issue{File: "skills/a/meta.yaml", Line: 2, Field: "stages.0", Code: repoformat.CodeUndefinedStage, Message: "no such stage"},
			"skills/a",
			extcheck.Finding{File: "meta.yaml", Line: 2, Message: msg.CheckStructureRepoUndefinedStage},
			map[string]any{"detail": "stages.0: no such stage"},
		},
		{
			"a code this version does not know",
			repoformat.Issue{File: "rigdepot.yaml", Code: "something-new", Message: "new"},
			"",
			extcheck.Finding{File: "rigdepot.yaml", Message: msg.CheckStructureRepoOther},
			map[string]any{"detail": "new", "code": "something-new"},
		},
	}
	for _, c := range cases {
		got := IssueFinding(c.is, c.res, extcheck.Advisory)
		c.want.Rule, c.want.Severity, c.want.Params = StructureRule, extcheck.Advisory, c.extra
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

func TestWithin(t *testing.T) {
	for p, want := range map[string]bool{
		"skills/a": true, "skills/a/SKILL.md": true, "skills/ab": false, "skills": false, "mcp/a.yaml": false,
	} {
		if Within(p, "skills/a") != want {
			t.Errorf("Within(%q, skills/a) = %v", p, !want)
		}
	}
}
