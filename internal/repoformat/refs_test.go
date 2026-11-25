// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package repoformat

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v4"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/bundle"
)

// fakeAgent is agents/<code>/ with an agent.yaml that refers to other resources and names
// directories that stay on the person's computer, the way the real agent kind does.
type fakeAgent struct{}

type agentSpec struct{ refs, local []string }

func (s agentSpec) Refs() []string  { return s.refs }
func (s agentSpec) Local() []string { return s.local }

var (
	_ kind.Referrer    = agentSpec{}
	_ kind.LocalKeeper = agentSpec{}
)

func (fakeAgent) ID() string          { return resource.KindAgent }
func (fakeAgent) Layout() kind.Layout { return kind.Layout{Dir: "agents"} }
func (fakeAgent) Schema() []byte      { return nil }
func (fakeAgent) Forms() []kind.Form  { return nil }

func (fakeAgent) Parse(_ resource.Ref, files fs.FS) (kind.Spec, error) {
	data, err := fs.ReadFile(files, "agent.yaml")
	if err != nil {
		return nil, err
	}
	var a struct{ Binds, Local []string }
	if err := yaml.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	return agentSpec{refs: a.Binds, local: a.Local}, nil
}

func refKinds() *kind.Registry {
	r := kind.NewRegistry()
	r.Register(fakeSkill{})
	r.Register(fakeAgent{})
	return r
}

func agent(binds string) fstest.MapFS {
	return fstest.MapFS{
		brand.RepoConfig:          {Data: []byte("schema: 1\n")},
		"skills/tdd/SKILL.md":     {Data: []byte("---\nname: tdd\n---\n")},
		"skills/tdd/meta.yaml":    {Data: []byte("name: TDD\n")},
		"skills/broken/SKILL.md":  {Data: []byte("no front matter\n")},
		"skills/broken/meta.yaml": {Data: []byte("name: Broken\n")},
		"agents/dev/meta.yaml":    {Data: []byte("name: Dev\n")},
		"agents/dev/agent.yaml":   {Data: []byte("binds: " + binds + "\n")},
		"agents/lead/meta.yaml":   {Data: []byte("name: Lead\n")},
		"agents/lead/agent.yaml":  {Data: []byte("binds: [dev]\n")},
	}
}

// A resource that refers to a code the repository does not have, to one that cannot be used, or
// to itself fails; so does whatever refers to it in turn.
func TestReadChecksReferences(t *testing.T) {
	for _, c := range []struct {
		binds, why string
	}{
		{"[tdd, nowhere]", `"nowhere", which is not a resource`},
		{"[broken]", `"broken", which cannot be used`},
		{"[dev]", "refers to itself"},
	} {
		repo, err := Read(agent(c.binds), Options{Kinds: refKinds()})
		if err != nil {
			t.Fatal(err)
		}
		var failed []string
		for _, f := range repo.Failed {
			failed = append(failed, f.Path)
			if f.Path == "agents/dev" && (f.Issues[0].Code != CodeUndefinedRef || !strings.Contains(f.Issues[0].Message, c.why)) {
				t.Errorf("binds %s: %+v, want %s", c.binds, f.Issues, c.why)
			}
		}
		if !slices.Equal(failed, []string{"agents/dev", "agents/lead", "skills/broken"}) {
			t.Errorf("binds %s: failed %v; the agent, the one that binds it, and the broken skill", c.binds, failed)
		}
	}
	repo, err := Read(agent("[tdd]"), Options{Kinds: refKinds()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.Lookup("lead"); !ok {
		t.Errorf("with every reference resolved, lead failed: %+v", repo.Failed)
	}
	// Read alone, the references are not looked at.
	repo, err = Read(agent("[nowhere]"), Options{Kinds: refKinds(), Alone: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.Lookup("dev"); !ok {
		t.Errorf("read alone, a reference failed the agent: %+v", repo.Failed)
	}
}

// The directories that stay on the person's computer are not part of the content digest.
func TestLocalDirectoriesAreNotInTheDigest(t *testing.T) {
	fsys := agent("[tdd]")
	fsys["agents/dev/agent.yaml"] = &fstest.MapFile{Data: []byte("binds: [tdd]\nlocal: [memory]\n")}
	without, err := Read(fsys, Options{Kinds: refKinds()})
	if err != nil {
		t.Fatal(err)
	}
	fsys["agents/dev/memory/README.md"] = &fstest.MapFile{Data: []byte("seed\n")}
	with, err := Read(fsys, Options{Kinds: refKinds()})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := without.Lookup("dev")
	b, _ := with.Lookup("dev")
	if a.Digest == "" || a.Digest != b.Digest {
		t.Errorf("digest %s without memory/, %s with it; want the same", a.Digest, b.Digest)
	}
	if _, err := fs.ReadFile(b.Files, "memory/README.md"); err != nil {
		t.Errorf("the seed in memory/ is not among the files: %v", err)
	}
	want, err := bundle.Digest(b.Files, "memory")
	if err != nil || b.Digest != want {
		t.Errorf("digest %s, want %s (%v)", b.Digest, want, err)
	}
}
