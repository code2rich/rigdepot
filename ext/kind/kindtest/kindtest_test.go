// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package kindtest

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// probe is a kind that exists only here: probes/<code>.yaml with a prompt and, optionally, a
// command and the name of a secret. It is an MCP server over stdio where the tool takes one and
// a skill elsewhere. Its files are flat "key: value" lines, which is all its fixtures use.
type probe struct{}

type probeSpec struct {
	ref            resource.Ref
	prompt         string
	command, token string
}

func (probe) ID() string          { return "probe" }
func (probe) Layout() kind.Layout { return kind.Layout{Dir: "probes", File: true, Ext: ".yaml"} }

func (probe) Schema() []byte {
	return []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["meta", "prompt"],
  "properties": {
    "meta": { "$ref": "../meta.schema.json" },
    "prompt": { "type": "string" },
    "command": { "type": "string" },
    "token_secret": { "type": "string" }
  }
}`)
}

func (probe) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	data, err := fs.ReadFile(files, ref.Code+".yaml")
	if err != nil {
		return nil, err
	}
	s := probeSpec{ref: ref}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, _ := strings.Cut(line, ":")
		value = strings.TrimSpace(value)
		if u, err := strconv.Unquote(value); err == nil {
			value = u
		}
		switch key {
		case "prompt":
			s.prompt = value
		case "command":
			s.command = value
		case "token_secret":
			s.token = value
		}
	}
	if s.prompt == "" {
		return nil, errors.New("prompt: is empty")
	}
	if strings.Contains(s.command, "${secret:") {
		return nil, errors.New("command: a secret may not be part of the command, which shows in the process list")
	}
	return s, nil
}

func (probe) Forms() []kind.Form {
	return []kind.Form{
		{Needs: kind.Needs{MCP: true}, Render: probeAsServer},
		{Needs: kind.Needs{Skills: true}, Degraded: "probe.as_skill", Render: probeAsSkill},
	}
}

func probeAsServer(s kind.Spec, caps adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
	p := s.(probeSpec)
	if p.command == "" || !caps.MCP.Stdio {
		return nil, kind.ErrNotApplicable
	}
	srv := adapter.MCPServer{Resource: p.ref, EntryName: opt.Prefix + p.ref.Code, Transport: adapter.Stdio, Command: p.command}
	if p.token != "" {
		srv.Env = map[string]adapter.Value{"PROBE_TOKEN": {Secret: p.token}}
	}
	return []adapter.Artifact{srv}, nil
}

func probeAsSkill(s kind.Spec, _ adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
	p := s.(probeSpec)
	name := opt.Prefix + p.ref.Code
	return []adapter.Artifact{adapter.SkillDir{
		Resource: p.ref,
		DirName:  name,
		Files:    []adapter.File{{Path: "SKILL.md", Data: []byte("---\nname: " + name + "\ndescription: A probe.\n---\n\n" + p.prompt + "\n")}},
		Degraded: "probe.as_skill",
	}}, nil
}

var _ kind.Kind = probe{}

// The well-behaved probe passes the whole suite. This is what a kind's TestKind looks like.
func TestKind(t *testing.T) {
	Run(t, probe{}, "testdata")
}

// record runs the suite against a kind and returns what failed, without failing this test.
func record(k kind.Kind) recorder {
	rec := newRecorder()
	rec.run("suite", func(r reporter) { runSuite(r, k, "testdata") })
	return rec
}

func TestRecorderSeesTheGoodKindPass(t *testing.T) {
	if rec := record(probe{}); len(*rec.failures) > 0 {
		t.Errorf("the well-behaved probe failed:\n%s", rec)
	}
}

// broken is the probe with one thing changed.
type broken struct {
	probe
	schema []byte
	parse  func(resource.Ref, fs.FS) (kind.Spec, error)
	forms  func([]kind.Form) []kind.Form
}

func (b broken) Schema() []byte {
	if b.schema != nil {
		return b.schema
	}
	return b.probe.Schema()
}

func (b broken) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	if b.parse != nil {
		return b.parse(ref, files)
	}
	return b.probe.Parse(ref, files)
}

func (b broken) Forms() []kind.Form {
	if b.forms != nil {
		return b.forms(b.probe.Forms())
	}
	return b.probe.Forms()
}

// mapRender changes what every form renders.
func mapRender(f func(form kind.Form, arts []adapter.Artifact, err error) ([]adapter.Artifact, error)) func([]kind.Form) []kind.Form {
	return func(forms []kind.Form) []kind.Form {
		out := slices.Clone(forms)
		for i, form := range out {
			render := form.Render
			out[i].Render = func(s kind.Spec, caps adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
				arts, err := render(s, caps, opt)
				if err != nil {
					return arts, err
				}
				return f(form, arts, nil)
			}
		}
		return out
	}
}

// mapArtifacts changes every artifact every form renders.
func mapArtifacts(f func(adapter.Artifact) adapter.Artifact) func([]kind.Form) []kind.Form {
	return mapRender(func(_ kind.Form, arts []adapter.Artifact, _ error) ([]adapter.Artifact, error) {
		out := make([]adapter.Artifact, len(arts))
		for i, a := range arts {
			out[i] = f(a)
		}
		return out, nil
	})
}

func renamed(a adapter.Artifact, name func(adapter.Artifact) string) adapter.Artifact {
	switch a := a.(type) {
	case adapter.SkillDir:
		a.DirName = name(a)
		return a
	case adapter.MCPServer:
		a.EntryName = name(a)
		return a
	}
	return a
}

// Each of these breaks the probe in one way. The assertion that guards against that way has to
// fail; if it does not, the suite would let such a kind through.
func TestBrokenKindsFailTheirAssertion(t *testing.T) {
	counter := 0
	cases := []struct {
		name string
		k    kind.Kind
		want check
	}{
		{"its schema is not JSON", broken{schema: []byte(`{"type": "object",`)}, checkSchema},
		{"its schema refers to a schema that does not exist", broken{schema: []byte(`{"$ref": "https://example.com/nothing.json"}`)}, checkSchema},
		{"it has no schema", broken{schema: []byte(" ")}, checkSchema},
		{"it has no forms", broken{forms: func([]kind.Form) []kind.Form { return nil }}, checkForms},
		{"a form has no Render", broken{forms: func(f []kind.Form) []kind.Form { f[0].Render = nil; return f }}, checkForms},
		{"a fallback names no message key", broken{forms: func(f []kind.Form) []kind.Form { f[1].Degraded = "as a skill"; return f }}, checkForms},
		{"it refuses what is valid", broken{parse: func(resource.Ref, fs.FS) (kind.Spec, error) { return nil, errors.New("no") }}, checkValid},
		{"it accepts what is not", broken{parse: func(ref resource.Ref, files fs.FS) (kind.Spec, error) {
			s, err := probe{}.Parse(ref, files)
			if err != nil {
				return probeSpec{ref: ref, prompt: "anything"}, nil
			}
			return s, nil
		}}, checkInvalid},
		{"its refusals do not say why", broken{parse: func(ref resource.Ref, files fs.FS) (kind.Spec, error) {
			s, err := probe{}.Parse(ref, files)
			if err != nil {
				return nil, errors.New("this resource cannot be used")
			}
			return s, nil
		}}, checkInvalid},
		{"a form fails", broken{forms: mapRender(func(kind.Form, []adapter.Artifact, error) ([]adapter.Artifact, error) {
			return nil, errors.New("cannot render")
		})}, checkRender},
		{"a form returns nothing instead of declining", broken{forms: mapRender(func(kind.Form, []adapter.Artifact, error) ([]adapter.Artifact, error) {
			return nil, nil
		})}, checkRender},
		{"every form declines", broken{forms: mapRender(func(kind.Form, []adapter.Artifact, error) ([]adapter.Artifact, error) {
			return nil, fmt.Errorf("probe: %w", kind.ErrNotApplicable)
		})}, checkDelivered},
		{"it forgets the prefix", broken{forms: mapArtifacts(func(a adapter.Artifact) adapter.Artifact {
			return renamed(a, func(a adapter.Artifact) string { return a.Ref().Code })
		})}, checkNames},
		{"it spells a prefix out", broken{forms: mapArtifacts(func(a adapter.Artifact) adapter.Artifact {
			return renamed(a, func(a adapter.Artifact) string { return "acme-" + a.Ref().Code })
		})}, checkNames},
		{"it names the artifact after something else", broken{forms: mapArtifacts(func(a adapter.Artifact) adapter.Artifact {
			return renamed(a, func(a adapter.Artifact) string { return a.Name() + "-server" })
		})}, checkNames},
		{"it loses the resource", broken{forms: mapArtifacts(func(a adapter.Artifact) adapter.Artifact {
			if s, ok := a.(adapter.SkillDir); ok {
				s.Resource = resource.Ref{Kind: "probe", Code: "other"}
				return s
			}
			return a
		})}, checkNames},
		{"its skill has no SKILL.md", broken{forms: mapArtifacts(func(a adapter.Artifact) adapter.Artifact {
			if s, ok := a.(adapter.SkillDir); ok {
				s.Files = []adapter.File{{Path: "README.md", Data: []byte("probe\n")}}
				return s
			}
			return a
		})}, checkArtifacts},
		{"its skill has a path that climbs out", broken{forms: mapArtifacts(func(a adapter.Artifact) adapter.Artifact {
			if s, ok := a.(adapter.SkillDir); ok {
				s.Files = append(slices.Clone(s.Files), adapter.File{Path: "../outside.md", Data: []byte("x")})
				return s
			}
			return a
		})}, checkArtifacts},
		{"its fallback does not say it is one", broken{forms: mapArtifacts(func(a adapter.Artifact) adapter.Artifact {
			if s, ok := a.(adapter.SkillDir); ok {
				s.Degraded = ""
				return s
			}
			return a
		})}, checkArtifacts},
		{"it gives a stdio server to a tool that takes only HTTP", broken{forms: func(f []kind.Form) []kind.Form {
			f[0].Render = func(s kind.Spec, caps adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
				caps.MCP.Stdio = true
				return probeAsServer(s, caps, opt)
			}
			return f
		}}, checkArtifacts},
		{"it hands out a pointer", broken{forms: mapArtifacts(func(a adapter.Artifact) adapter.Artifact {
			if s, ok := a.(adapter.SkillDir); ok {
				return &s
			}
			return a
		})}, checkArtifacts},
		{"it leaves the secret reference as text", broken{forms: mapArtifacts(func(a adapter.Artifact) adapter.Artifact {
			if s, ok := a.(adapter.MCPServer); ok && len(s.Env) > 0 {
				s.Env = map[string]adapter.Value{"PROBE_TOKEN": {Literal: "${secret:PROBE_TOKEN}"}}
				return s
			}
			return a
		})}, checkSecrets},
		{"it leaves a secret reference in the arguments", broken{forms: mapArtifacts(func(a adapter.Artifact) adapter.Artifact {
			if s, ok := a.(adapter.MCPServer); ok {
				s.Args = []string{"--token", "${secret:PROBE_TOKEN}"}
				return s
			}
			return a
		})}, checkSecrets},
		{"it renders differently every time", broken{forms: mapArtifacts(func(a adapter.Artifact) adapter.Artifact {
			if s, ok := a.(adapter.SkillDir); ok {
				counter++
				s.Files = append(slices.Clone(s.Files), adapter.File{Path: "run.txt", Data: []byte(strconv.Itoa(counter))})
				return s
			}
			return a
		})}, checkDeterministic},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := record(c.k)
			got := rec.checks()
			if got[c.want] == 0 {
				t.Errorf("[%s] did not fail; failed: %v\n%s", c.want, slices.Sorted(maps.Keys(got)), rec)
			}
		})
	}
}

// Fixtures the suite cannot run are reported, not skipped.
func TestBadFixturesAreReported(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
	}{
		{"no valid fixtures", map[string]string{"invalid/prompt--empty.yaml": "meta: {name: x}\nprompt: \"\"\n"}},
		{"no invalid fixtures", map[string]string{"valid/plain.yaml": "meta: {name: x}\nprompt: \"hi\"\n"}},
		{"a fixture of the wrong shape", map[string]string{
			"valid/plain.yaml":           "meta: {name: x}\nprompt: \"hi\"\n",
			"valid/notes.txt":            "not a probe\n",
			"invalid/prompt--empty.yaml": "meta: {name: x}\nprompt: \"\"\n",
		}},
		{"an invalid fixture that names no field", map[string]string{
			"valid/plain.yaml":   "meta: {name: x}\nprompt: \"hi\"\n",
			"invalid/empty.yaml": "meta: {name: x}\nprompt: \"\"\n",
		}},
		{"a valid fixture whose name is not a code", map[string]string{
			"valid/Plain_Probe.yaml":     "meta: {name: x}\nprompt: \"hi\"\n",
			"invalid/prompt--empty.yaml": "meta: {name: x}\nprompt: \"\"\n",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for p, body := range c.files {
				p = filepath.Join(dir, filepath.FromSlash(p))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			rec := newRecorder()
			rec.run("suite", func(r reporter) { runSuite(r, probe{}, dir) })
			if rec.checks()[checkFixtures] == 0 {
				t.Errorf("[fixtures] did not fail:\n%s", rec)
			}
		})
	}
}
