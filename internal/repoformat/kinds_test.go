// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package repoformat

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"

	"go.yaml.in/yaml/v4"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
)

// The real kinds live in builtin/kinds, which the core must not import. These stand-ins have the
// same layout and do the least a kind does: refuse what they cannot use.

// fakeSkill is skills/<code>/ with a SKILL.md whose front matter names the directory.
type fakeSkill struct{}

type skillSpec struct {
	ref   resource.Ref
	files fs.FS
}

func (fakeSkill) ID() string          { return resource.KindSkill }
func (fakeSkill) Layout() kind.Layout { return kind.Layout{Dir: "skills"} }
func (fakeSkill) Schema() []byte      { return nil }
func (fakeSkill) Forms() []kind.Form  { return nil }

func (fakeSkill) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	data, err := fs.ReadFile(files, "SKILL.md")
	if err != nil {
		return nil, fmt.Errorf("a skill needs a SKILL.md: %w", err)
	}
	// A work tree checked out with CRLF line endings is still a skill.
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	front, _, ok2 := bytes.Cut(rest, []byte("\n---"))
	if !ok || !ok2 {
		return nil, errors.New("SKILL.md has no front matter")
	}
	var fm struct{ Name string }
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return nil, err
	}
	if fm.Name != ref.Code {
		return nil, fmt.Errorf("the name in SKILL.md is %q, not %q", fm.Name, ref.Code)
	}
	return skillSpec{ref: ref, files: files}, nil
}

// fakeMCP is mcp/<code>.yaml with a schema of its own that refers to the metadata schema.
type fakeMCP struct{}

func (fakeMCP) ID() string          { return resource.KindMCP }
func (fakeMCP) Layout() kind.Layout { return kind.Layout{Dir: "mcp", File: true, Ext: ".yaml"} }
func (fakeMCP) Forms() []kind.Form  { return nil }

func (fakeMCP) Schema() []byte {
	return []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["meta", "server"],
  "properties": {
    "meta": { "$ref": "https://rigdepot.code2rich.com/schema/repo/v1/meta.schema.json" },
    "server": {
      "type": "object",
      "required": ["type"],
      "properties": {
        "type": { "enum": ["stdio", "http", "sse"] },
        "url": { "type": "string" },
        "headers": { "type": "object", "additionalProperties": { "type": "string" } },
        "command": { "type": "string" },
        "args": { "type": "array", "items": { "type": "string" } },
        "env": { "type": "object", "additionalProperties": { "type": "string" } }
      }
    },
    "delivery": { "enum": ["native", "on-demand"] },
    "tools": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": { "name": { "type": "string" }, "summary": { "type": "string" }, "writes": { "type": "boolean" } }
      }
    }
  }
}`)
}

func (fakeMCP) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	data, err := fs.ReadFile(files, ref.Code+".yaml")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Server struct {
			Type, URL, Command string
		}
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Server.Type == "stdio" && doc.Server.Command == "" {
		return nil, errors.New("a stdio server needs a command")
	}
	return doc.Server, nil
}

func testKinds() *kind.Registry {
	r := kind.NewRegistry()
	r.Register(fakeSkill{})
	r.Register(fakeMCP{})
	return r
}
