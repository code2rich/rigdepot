// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package agent

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/kind/kindtest"
	"github.com/code2rich/rigdepot/ext/resource"
)

var update = flag.Bool("update", false, "write testdata/golden from what the kind renders")

func TestMain(m *testing.M) { kindtest.Main(m) }

func TestKind(t *testing.T) { kindtest.Run(t, New(), "testdata") }

var opt = kind.RenderOptions{Prefix: "acme-", CLI: "acme", Data: "~/.acme"}

func parse(t *testing.T, code string) spec {
	t.Helper()
	s, err := New().Parse(resource.Ref{Kind: resource.KindAgent, Code: code}, os.DirFS(filepath.Join("testdata", "valid", code)))
	if err != nil {
		t.Fatal(err)
	}
	return s.(spec)
}

func deliver(t *testing.T, s kind.Spec, caps adapter.Capabilities) (kind.Form, []adapter.Artifact) {
	t.Helper()
	for _, f := range New().Forms() {
		if !f.Needs.SatisfiedBy(caps) {
			continue
		}
		arts, err := f.Render(s, caps, opt)
		if errors.Is(err, kind.ErrNotApplicable) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		return f, arts
	}
	return kind.Form{}, nil
}

func TestReferencesAndLocalDirectories(t *testing.T) {
	s := parse(t, "backend-developer")
	if got := s.Refs(); !slices.Equal(got, []string{"eng-handbook", "gitlab", "unit-testing"}) {
		t.Errorf("Refs = %v", got)
	}
	if got := s.Local(); !slices.Equal(got, []string{"memory", "workspace"}) {
		t.Errorf("Local = %v; written without the trailing slash", got)
	}
}

// Which form, by tool: a sub-agent with its directory, a sub-agent alone, the whole agent as a
// skill.
func TestFormsByTool(t *testing.T) {
	dev, reviewer := parse(t, "backend-developer"), parse(t, "code-reviewer")
	shapes := func(arts []adapter.Artifact) string {
		var out []string
		for _, a := range arts {
			switch x := a.(type) {
			case adapter.SubAgent:
				out = append(out, "agent")
			case adapter.SkillDir:
				out = append(out, "skill"+map[bool]string{true: "(" + x.Degraded + ")"}[x.Degraded != ""])
			}
		}
		return strings.Join(out, "+")
	}
	both := adapter.Capabilities{Skills: true, Agents: true}
	for _, c := range []struct {
		s    spec
		caps adapter.Capabilities
		want string
	}{
		{dev, both, "agent+skill"},
		{reviewer, both, "agent"},
		{dev, adapter.Capabilities{Agents: true}, ""},
		{reviewer, adapter.Capabilities{Agents: true}, "agent"},
		{dev, adapter.Capabilities{Skills: true}, "skill(" + AsSkill + ")"},
		{reviewer, adapter.Capabilities{Skills: true}, "skill(" + AsSkill + ")"},
	} {
		_, arts := deliver(t, c.s, c.caps)
		if got := shapes(arts); got != c.want {
			t.Errorf("%s for %+v: %q, want %q", c.s.ref.Code, c.caps, got, c.want)
		}
	}
}

// The agent's directory carries its own skills, the seeds of its local directories and its
// .env.example, and names the local directories so that an update keeps what is in them. Not
// agent.yaml, meta.yaml or the entry: those are about the agent, and the entry's text is in the
// sub-agent or the skill.
func TestTheDirectory(t *testing.T) {
	for _, caps := range []adapter.Capabilities{{Skills: true, Agents: true}, {Skills: true}} {
		_, arts := deliver(t, parse(t, "backend-developer"), caps)
		sd := arts[len(arts)-1].(adapter.SkillDir)
		var paths []string
		for _, f := range sd.Files {
			paths = append(paths, f.Path)
		}
		slices.Sort(paths)
		if want := []string{".env.example", "SKILL.md", "memory/README.md", "skills/api-development/SKILL.md"}; !slices.Equal(paths, want) {
			t.Errorf("%+v: files %v, want %v", caps, paths, want)
		}
		if !slices.Equal(sd.Local, []string{"memory", "workspace"}) || sd.DirName != "acme-backend-developer" {
			t.Errorf("%+v: %s, local %v", caps, sd.DirName, sd.Local)
		}
	}
}

// The texts are reviewed in the golden files: go test -run Golden -update.
func TestGolden(t *testing.T) {
	s := parse(t, "backend-developer")
	_, native := deliver(t, s, adapter.Capabilities{Skills: true, Agents: true})
	_, fallback := deliver(t, s, adapter.Capabilities{Skills: true})
	sub := native[0].(adapter.SubAgent)
	for name, data := range map[string][]byte{
		"backend-developer.agent.md":       []byte("name: " + sub.AgentName + "\ndescription: " + sub.Description + "\n---\n" + sub.Body),
		"backend-developer.files.SKILL.md": native[1].(adapter.SkillDir).Files[0].Data,
		"backend-developer.SKILL.md":       fallback[0].(adapter.SkillDir).Files[0].Data,
	} {
		golden := filepath.Join("testdata", "golden", name)
		if *update {
			if err := os.WriteFile(golden, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, want) {
			t.Errorf("%s differs:\n%s", golden, data)
		}
	}
	if !strings.Contains(sub.Body, "每个改动都要用开发自测证明") || !strings.Contains(sub.Body, "`acme-backend-developer`") {
		t.Errorf("the sub-agent's body has the instructions and points at its files:\n%s", sub.Body)
	}
}
