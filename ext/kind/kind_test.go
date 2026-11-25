// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package kind

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// prompt is the example kind from the contributor guide: prompts/<code>.md, one file, delivered
// as a skill directory. That it compiles against this package is part of the contract.
type prompt struct{ id string }

func (p prompt) ID() string   { return p.id }
func (prompt) Layout() Layout { return Layout{Dir: "prompts", File: true, Ext: ".md"} }
func (prompt) Schema() []byte { return []byte(`{"type":"object"}`) }
func (prompt) Forms() []Form  { return []Form{{Needs: Needs{Skills: true}, Render: promptAsSkill}} }

type promptSpec struct {
	ref  resource.Ref
	body []byte
}

func (prompt) Parse(ref resource.Ref, files fs.FS) (Spec, error) {
	body, err := fs.ReadFile(files, ref.Code+".md")
	if err != nil {
		return nil, fmt.Errorf("prompt %s: %w", ref.Code, err)
	}
	return promptSpec{ref: ref, body: body}, nil
}

func promptAsSkill(s Spec, _ adapter.Capabilities, opt RenderOptions) ([]adapter.Artifact, error) {
	p := s.(promptSpec)
	if len(p.body) == 0 {
		return nil, ErrNotApplicable
	}
	return []adapter.Artifact{adapter.SkillDir{
		Resource: p.ref,
		DirName:  opt.Prefix + p.ref.Code,
		Files:    []adapter.File{{Path: "SKILL.md", Data: p.body}},
	}}, nil
}

var _ Kind = prompt{}

func TestParseAndRenderRoundTrip(t *testing.T) {
	k := prompt{id: "prompt"}
	ref := resource.Ref{Kind: k.ID(), Code: "release-notes"}
	// A single-file resource arrives as a directory that holds its one file.
	files := fstest.MapFS{"release-notes.md": {Data: []byte("# Release notes\n")}}
	spec, err := k.Parse(ref, files)
	if err != nil {
		t.Fatal(err)
	}
	form := k.Forms()[0]
	artifacts, err := form.Render(spec, adapter.Capabilities{Skills: true}, RenderOptions{Prefix: "acme-"})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("Render returned %d artifacts, want 1", len(artifacts))
	}
	dir, ok := artifacts[0].(adapter.SkillDir)
	if !ok {
		t.Fatalf("Render returned a %T, want a SkillDir", artifacts[0])
	}
	if dir.Name() != "acme-release-notes" || dir.Ref() != ref {
		t.Errorf("artifact is %q for %v, want acme-release-notes for %v", dir.Name(), dir.Ref(), ref)
	}
	if string(dir.Files[0].Data) != "# Release notes\n" {
		t.Errorf("content changed on the way: %q", dir.Files[0].Data)
	}

	if _, err := k.Parse(resource.Ref{Kind: k.ID(), Code: "missing"}, files); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Parse of a resource whose file is missing: %v, want an error that wraps fs.ErrNotExist", err)
	}
}

// A form can decline one particular resource, and the caller can tell that from a failure.
func TestRenderCanDeclineAForm(t *testing.T) {
	_, err := promptAsSkill(promptSpec{}, adapter.Capabilities{Skills: true}, RenderOptions{})
	if !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("Render of an empty prompt: %v, want ErrNotApplicable", err)
	}
	wrapped := fmt.Errorf("mcp jira: on-demand delivery: %w", ErrNotApplicable)
	if !errors.Is(wrapped, ErrNotApplicable) {
		t.Error("ErrNotApplicable cannot be recognised once it is wrapped")
	}
}

func TestNeedsSatisfiedBy(t *testing.T) {
	all := adapter.Capabilities{Skills: true, Commands: true, Agents: true, MCP: adapter.MCPCaps{Stdio: true, HTTP: true}}
	cases := []struct {
		name  string
		needs Needs
		caps  adapter.Capabilities
		want  bool
	}{
		{"nothing needed, nothing supported", Needs{}, adapter.Capabilities{}, true},
		{"skills needed and supported", Needs{Skills: true}, adapter.Capabilities{Skills: true}, true},
		{"skills needed, nothing supported", Needs{Skills: true}, adapter.Capabilities{}, false},
		{"commands needed, only skills supported", Needs{Commands: true}, adapter.Capabilities{Skills: true}, false},
		{"commands needed and supported", Needs{Commands: true}, adapter.Capabilities{Commands: true}, true},
		{"agents needed, only commands supported", Needs{Agents: true}, adapter.Capabilities{Commands: true}, false},
		{"agents needed and supported", Needs{Agents: true}, adapter.Capabilities{Agents: true}, true},
		{"MCP needed, stdio only", Needs{MCP: true}, adapter.Capabilities{MCP: adapter.MCPCaps{Stdio: true}}, true},
		{"MCP needed, HTTP only", Needs{MCP: true}, adapter.Capabilities{MCP: adapter.MCPCaps{HTTP: true}}, true},
		{"MCP needed, no transport", Needs{MCP: true}, adapter.Capabilities{Skills: true, MCP: adapter.MCPCaps{EnvRef: true, ToolSearch: true}}, false},
		{"two needed, one supported", Needs{Skills: true, Commands: true}, adapter.Capabilities{Skills: true}, false},
		{"everything needed and supported", Needs{Skills: true, Commands: true, Agents: true, MCP: true}, all, true},
	}
	for _, c := range cases {
		if got := c.needs.SatisfiedBy(c.caps); got != c.want {
			t.Errorf("%s: SatisfiedBy = %v, want %v", c.name, got, c.want)
		}
	}
}

// The order of Forms is the fallback rule. This walks it the way the core does, with the
// workflow example from the package documentation.
func TestFormsAreTriedInOrder(t *testing.T) {
	render := func(Spec, adapter.Capabilities, RenderOptions) ([]adapter.Artifact, error) { return nil, nil }
	forms := []Form{
		{Needs: Needs{Commands: true}, Render: render},
		{Needs: Needs{Skills: true}, Degraded: "workflow.as_skill", Render: render},
	}
	first := func(caps adapter.Capabilities) (degraded string, found bool) {
		for _, f := range forms {
			if f.Needs.SatisfiedBy(caps) {
				return f.Degraded, true
			}
		}
		return "", false
	}
	if degraded, ok := first(adapter.Capabilities{Skills: true, Commands: true}); !ok || degraded != "" {
		t.Errorf("a tool with slash commands gets %q, %v; want the native form", degraded, ok)
	}
	if degraded, ok := first(adapter.Capabilities{Skills: true}); !ok || degraded != "workflow.as_skill" {
		t.Errorf("a tool without slash commands gets %q, %v; want the fallback as a skill", degraded, ok)
	}
	if _, ok := first(adapter.Capabilities{}); ok {
		t.Error("a tool that takes nothing was given a form")
	}
}

func kindIDs(kinds []Kind) []string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, k.ID())
	}
	return out
}

// mustPanic runs fn and returns what it panicked with. The test fails if fn returns normally.
func mustPanic(t *testing.T, what string, fn func()) (msg string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("%s did not panic", what)
		}
		msg, _ = r.(string)
	}()
	fn()
	return ""
}

func TestRegistryReturnsKindsSortedByID(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"workflow", "skill", "mcp", "agent"} {
		r.Register(prompt{id: id})
	}
	if got, want := kindIDs(r.All()), []string{"agent", "mcp", "skill", "workflow"}; !slices.Equal(got, want) {
		t.Errorf("All() = %v, want %v", got, want)
	}
}

func TestRegistryPanicsOnDuplicateID(t *testing.T) {
	r := NewRegistry()
	r.Register(prompt{id: "skill"})
	msg := mustPanic(t, "registering the ID skill twice", func() { r.Register(prompt{id: "skill"}) })
	if !strings.Contains(msg, "resource kind") || !strings.Contains(msg, `"skill"`) {
		t.Errorf("panic message %q does not say which kind is registered twice", msg)
	}
}

func TestRegistryFreezesOnFirstRead(t *testing.T) {
	t.Run("All", func(t *testing.T) {
		r := NewRegistry()
		r.Register(prompt{id: "skill"})
		r.All()
		mustPanic(t, "registering after All", func() { r.Register(prompt{id: "mcp"}) })
		if got := kindIDs(r.All()); !slices.Equal(got, []string{"skill"}) {
			t.Errorf("the late kind got in anyway: All() = %v", got)
		}
	})
	t.Run("Lookup", func(t *testing.T) {
		r := NewRegistry()
		r.Register(prompt{id: "skill"})
		if k, ok := r.Lookup("skill"); !ok || k.ID() != "skill" {
			t.Fatalf(`Lookup("skill") = %v, %v`, k, ok)
		}
		if _, ok := r.Lookup("mcp"); ok {
			t.Fatal(`Lookup("mcp") found a kind that was never registered`)
		}
		mustPanic(t, "registering after Lookup", func() { r.Register(prompt{id: "mcp"}) })
	})
}

func TestRegistryPanicsOnNilAndMalformedID(t *testing.T) {
	r := NewRegistry()
	mustPanic(t, "Register(nil)", func() { r.Register(nil) })
	for _, id := range []string{"", "Skill", "my_kind"} {
		msg := mustPanic(t, "registering the ID "+id, func() { r.Register(prompt{id: id}) })
		if !strings.Contains(msg, "malformed") {
			t.Errorf("Register with ID %q: panic message %q", id, msg)
		}
	}
	if got := r.All(); len(got) != 0 {
		t.Errorf("a refused kind is in the registry: %v", kindIDs(got))
	}
}

// The package-level functions are the registry of the running program. This is the only test
// that touches it; nothing registers kinds in this test binary.
func TestDefaultRegistry(t *testing.T) {
	Register(prompt{id: "skill"})
	Register(prompt{id: "mcp"})
	if got, want := kindIDs(All()), []string{"mcp", "skill"}; !slices.Equal(got, want) {
		t.Errorf("All() = %v, want %v", got, want)
	}
	if k, ok := Lookup("mcp"); !ok || k.ID() != "mcp" {
		t.Errorf(`Lookup("mcp") = %v, %v`, k, ok)
	}
	mustPanic(t, "Register after the default registry was read", func() { Register(prompt{id: "agent"}) })
}
