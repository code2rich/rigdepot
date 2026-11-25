// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/repoformat"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// readWithProgramKinds reads a resource repository with the kinds this program links, the way
// rgd will, and renders every resource for a tool that takes everything.
func readWithProgramKinds(t *testing.T, dir string) map[string]string {
	t.Helper()
	repo, err := repoformat.Read(os.DirFS(dir), repoformat.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range repo.Failed {
		t.Errorf("%s failed: %v", f.Path, f.Issues)
	}
	for _, n := range repo.Notices {
		if n.Code == repoformat.CodeUnknownDir {
			t.Errorf("no kind of this program reads %s", n.File)
		}
	}
	caps := adapter.Capabilities{Skills: true, MCP: adapter.MCPCaps{Stdio: true, HTTP: true, EnvRef: true}}
	opt := kind.RenderOptions{Prefix: brand.Prefix, CLI: brand.CLI}
	got := map[string]string{}
	for _, res := range repo.Resources {
		for _, f := range res.Kind.Forms() {
			if !f.Needs.SatisfiedBy(caps) {
				continue
			}
			arts, err := f.Render(res.Spec, caps, opt)
			if errors.Is(err, kind.ErrNotApplicable) {
				continue
			}
			if err != nil || len(arts) != 1 {
				t.Errorf("%v: %d artifacts, %v", res.Ref, len(arts), err)
				break
			}
			switch a := arts[0].(type) {
			case adapter.SkillDir:
				got[res.Ref.String()] = "skill " + a.DirName
			case adapter.MCPServer:
				got[res.Ref.String()] = "server " + a.EntryName
			}
			break
		}
	}
	return got
}

// The program knows skills and MCP servers: a repository with both is read and every resource
// renders.
func TestProgramReadsSkillsAndMCPServers(t *testing.T) {
	got := readWithProgramKinds(t, "testdata/repo")
	want := map[string]string{
		"skill/unit-testing": "skill " + brand.Prefix + "unit-testing",
		"mcp/jira":           "server " + brand.Prefix + "jira",
		"mcp/gitlab":         "skill " + brand.Prefix + "gitlab", // delivered on demand
	}
	for ref, w := range want {
		if got[ref] != w {
			t.Errorf("%s: %q, want %q", ref, got[ref], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("rendered %v, want %v", got, want)
	}
}

// The example repository the documentation and the development setup use, once it exists.
func TestProgramReadsTheExampleRepository(t *testing.T) {
	const dir = "../../examples/resources"
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		t.Skip("examples/resources does not exist yet")
	}
	got := readWithProgramKinds(t, dir)
	var kinds []string
	for ref := range got {
		k, _, _ := cut(ref)
		if !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
	}
	slices.Sort(kinds)
	if !slices.Equal(kinds, []string{"mcp", "skill"}) {
		t.Errorf("rendered %v; want skills and MCP servers", got)
	}
}

func cut(ref string) (kind, rest string, ok bool) {
	for i := range len(ref) {
		if ref[i] == '/' {
			return ref[:i], ref[i+1:], true
		}
	}
	return ref, "", false
}
