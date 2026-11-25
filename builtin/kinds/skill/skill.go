// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package skill

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v4"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
)

// File is the file that makes a directory a skill.
const File = "SKILL.md"

// metaFile is the product's metadata about the skill. It stays in the repository.
const metaFile = "meta.yaml"

//go:embed schema.json
var schemaJSON []byte

// New returns the skill kind.
func New() kind.Kind { return skillKind{} }

type skillKind struct{}

// spec is a parsed skill: the files it is delivered with.
type spec struct {
	ref   resource.Ref
	files []adapter.File
}

func (skillKind) ID() string          { return resource.KindSkill }
func (skillKind) Layout() kind.Layout { return kind.Layout{Dir: "skills"} }
func (skillKind) Schema() []byte      { return slices.Clone(schemaJSON) }

func (skillKind) Forms() []kind.Form {
	return []kind.Form{{Needs: kind.Needs{Skills: true}, Render: asSkill}}
}

// Parse checks the front matter of SKILL.md against the schema and the directory name, and takes
// the skill's files.
func (skillKind) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	data, err := fs.ReadFile(files, File)
	if err != nil {
		return nil, fmt.Errorf("%s: a skill needs one: %w", File, err)
	}
	front, err := frontMatter(data)
	if err != nil {
		return nil, err
	}
	if err := validate(front); err != nil {
		return nil, err
	}
	if name := front.(map[string]any)["name"]; name != ref.Code {
		return nil, fmt.Errorf("%s: the name in the front matter is %q; it has to equal the directory name %q", File, name, ref.Code)
	}
	s := spec{ref: ref}
	err = fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == metaFile {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(files, p)
		if err != nil {
			return err
		}
		s.files = append(s.files, adapter.File{Path: p, Data: data, Exec: info.Mode().Perm()&0o111 != 0})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("skill %s: %w", ref.Code, err)
	}
	return s, nil
}

func asSkill(s kind.Spec, _ adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
	sk := s.(spec)
	return []adapter.Artifact{adapter.SkillDir{
		Resource: sk.ref,
		DirName:  opt.Prefix + sk.ref.Code,
		Files:    slices.Clone(sk.files),
	}}, nil
}

// frontMatter returns the front matter of a SKILL.md as a JSON value. Line endings and a byte
// order mark do not matter here; the file itself is delivered as it is.
func frontMatter(data []byte) (any, error) {
	text := bytes.TrimPrefix(data, []byte("\uFEFF"))
	text = bytes.ReplaceAll(text, []byte("\r\n"), []byte("\n"))
	rest, ok := bytes.CutPrefix(text, []byte("---\n"))
	if !ok {
		return nil, fmt.Errorf("%s has no front matter: its first line has to be ---", File)
	}
	var front []byte
	for {
		line, after, found := bytes.Cut(rest, []byte("\n"))
		if string(bytes.TrimRight(line, " \t")) == "---" {
			break
		}
		if !found {
			return nil, fmt.Errorf("%s: the front matter does not end with a line ---", File)
		}
		front = append(append(front, line...), '\n')
		rest = after
	}
	var v any
	if err := yaml.Unmarshal(front, &v); err != nil {
		return nil, fmt.Errorf("%s: the front matter is not YAML: %w", File, err)
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, fmt.Errorf("%s: the front matter has to be a mapping with at least name and description", File)
	}
	// Through JSON, so that the schema sees what a JSON Schema validator expects.
	js, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%s: the front matter cannot be read: %w", File, err)
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(js))
}

var compiled = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, err
	}
	id, _ := doc.(map[string]any)["$id"].(string)
	c := jsonschema.NewCompiler()
	c.UseLoader(jsonschema.SchemeURLLoader{}) // nothing is ever fetched
	if err := c.AddResource(id, doc); err != nil {
		return nil, err
	}
	return c.Compile(id)
})

// validate checks the front matter against the schema. The error names every field that does
// not fit.
func validate(front any) error {
	sch, err := compiled()
	if err != nil {
		return fmt.Errorf("the schema of the skill kind: %w", err)
	}
	err = sch.Validate(front)
	var verr *jsonschema.ValidationError
	if err == nil || !errors.As(err, &verr) {
		return err
	}
	var problems []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) > 0 {
			for _, c := range e.Causes {
				walk(c)
			}
			return
		}
		msg := ""
		if out := e.BasicOutput(); out.Error != nil {
			msg = out.Error.String()
		}
		if len(e.InstanceLocation) > 0 {
			msg = strings.Join(e.InstanceLocation, ".") + ": " + msg
		}
		problems = append(problems, msg)
	}
	walk(verr)
	slices.Sort(problems)
	return fmt.Errorf("%s: the front matter does not fit: %s", File, strings.Join(slices.Compact(problems), "; "))
}
