// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package catalogtest gives the catalog's tests resource kinds and check rules. The real ones
// live in builtin/, which the core must not import; these do the least a kind or a rule does,
// in the same shapes: a directory kind (skills/<code>/ with a SKILL.md), a one-document kind
// that refers to secrets (mcp/<code>.yaml) and one that refers to other resources
// (workflows/<code>.yaml).
package catalogtest

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"

	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
)

// Kinds returns a registry with the three test kinds.
func Kinds() *kind.Registry {
	r := kind.NewRegistry()
	r.Register(skill{})
	r.Register(mcp{})
	r.Register(workflow{})
	return r
}

// Rules returns rule set version 1: "secrets" (must fix, any line with "password="), the rule
// whose findings can be waived, and "description" (advisory, a resource without "when").
func Rules() extcheck.Ruleset {
	return extcheck.Ruleset{Version: 1, Rules: []extcheck.Rule{description{}, secrets{}}}
}

type skill struct{}

func (skill) ID() string          { return resource.KindSkill }
func (skill) Layout() kind.Layout { return kind.Layout{Dir: "skills"} }
func (skill) Schema() []byte      { return nil }
func (skill) Forms() []kind.Form  { return nil }
func (skill) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	if _, err := fs.Stat(files, "SKILL.md"); err != nil {
		return nil, errors.New("SKILL.md is missing")
	}
	return ref, nil
}

var secretRef = regexp.MustCompile(`\$\{secret:([A-Z][A-Z0-9_]+)\}`)

type mcp struct{}

type mcpSpec struct{ secrets []string }

func (s mcpSpec) Secrets() []string { return s.secrets }

func (mcp) ID() string          { return resource.KindMCP }
func (mcp) Layout() kind.Layout { return kind.Layout{Dir: "mcp", File: true, Ext: ".yaml"} }
func (mcp) Schema() []byte {
	return []byte(`{"type":"object","required":["meta","server"],"properties":{"server":{"type":"object","required":["type"]}}}`)
}
func (mcp) Forms() []kind.Form { return nil }
func (mcp) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	data, err := fs.ReadFile(files, ref.Code+".yaml")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range secretRef.FindAllSubmatch(data, -1) {
		out = append(out, string(m[1]))
	}
	slices.Sort(out)
	return mcpSpec{secrets: slices.Compact(out)}, nil
}

type workflow struct{}

type workflowSpec struct{ refs []string }

// Refs is what the catalog reads to list what a resource uses (kind.Referrer).
func (s workflowSpec) Refs() []string { return s.refs }

func (workflow) ID() string          { return resource.KindWorkflow }
func (workflow) Layout() kind.Layout { return kind.Layout{Dir: "workflows", File: true, Ext: ".yaml"} }
func (workflow) Schema() []byte      { return []byte(`{"type":"object","required":["meta","steps"]}`) }
func (workflow) Forms() []kind.Form  { return nil }
func (workflow) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	data, err := fs.ReadFile(files, ref.Code+".yaml")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Steps []struct {
			Use string `yaml:"use"`
		} `yaml:"steps"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var s workflowSpec
	for _, st := range doc.Steps {
		s.refs = append(s.refs, st.Use)
	}
	slices.Sort(s.refs)
	s.refs = slices.Compact(s.refs)
	return s, nil
}

type secrets struct{}

func (secrets) ID() string                  { return "secrets" }
func (secrets) Severity() extcheck.Severity { return extcheck.MustFix }
func (secrets) Applies(string) bool         { return true }
func (secrets) Check(_ context.Context, in extcheck.Input) ([]extcheck.Finding, error) {
	var out []extcheck.Finding
	err := fs.WalkDir(in.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(in.Files, p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "password=") {
				out = append(out, extcheck.Finding{Rule: "secrets", Severity: extcheck.MustFix, File: p, Line: i + 1,
					Message: "check.secrets.assignment", Params: map[string]any{"key": "password", "length": 8}})
			}
		}
		return nil
	})
	return out, err
}

type description struct{}

func (description) ID() string                  { return "description" }
func (description) Severity() extcheck.Severity { return extcheck.Advisory }
func (description) Applies(string) bool         { return true }
func (description) Check(_ context.Context, in extcheck.Input) ([]extcheck.Finding, error) {
	if in.Meta.When != "" {
		return nil, nil
	}
	return []extcheck.Finding{{Rule: "description", Severity: extcheck.Advisory, Message: "check.description.no_when"}}, nil
}
