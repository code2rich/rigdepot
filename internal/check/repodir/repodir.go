// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package repodir reads a resource repository from a directory into the engine's in-memory
// resources. It exists for tests: the rule fixtures, the example repositories and the engine's
// own tests are directories laid out as a resource repository.
//
// It is a stand-in for internal/repoformat and deliberately does less: no schema validation, no
// path safety checks, no digest. It also reads what repoformat would refuse, such as a skill
// without SKILL.md or an MCP file that is not YAML, because those are exactly what the structure
// rule has to see. Programs read repositories through repoformat; this package must not grow
// into a second reader.
package repodir

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/check"
)

// layout is where each kind lives in a repository: a directory per resource, or one file per
// resource with the given extension.
var layout = []struct {
	dir, kind, ext string
}{
	{"agents", resource.KindAgent, ""},
	{"knowledge", resource.KindKnowledge, ".yaml"},
	{"mcp", resource.KindMCP, ".yaml"},
	{"skills", resource.KindSkill, ""},
	{"workflows", resource.KindWorkflow, ".yaml"},
}

// Load reads every resource in the repository at the root of fsys, sorted by kind directory and
// code. Top-level directories that are not a kind, files at the top level (rigdepot.yaml,
// README.md) and entries whose names start with a dot are skipped.
//
// Metadata is read from meta.yaml for directory-shaped kinds and from the "meta" key for
// single-file kinds. Metadata that cannot be parsed is left empty: the files are still all
// there for the rules to judge. An error means the directory itself could not be read.
func Load(fsys fs.FS) ([]check.Resource, error) {
	var out []check.Resource
	for _, l := range layout {
		entries, err := fs.ReadDir(fsys, l.dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			var res check.Resource
			switch {
			case l.ext == "" && e.IsDir():
				res, err = loadDir(fsys, l.kind, path.Join(l.dir, name), name)
			case l.ext != "" && !e.IsDir() && strings.HasSuffix(name, l.ext):
				res, err = loadFile(fsys, l.kind, path.Join(l.dir, name), strings.TrimSuffix(name, l.ext))
			default:
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, res)
		}
	}
	return out, nil
}

func loadDir(fsys fs.FS, kind, dir, code string) (check.Resource, error) {
	res := check.Resource{Ref: resource.Ref{Kind: kind, Code: code}, Files: map[string][]byte{}}
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		res.Files[strings.TrimPrefix(p, dir+"/")] = data
		return nil
	})
	if err != nil {
		return check.Resource{}, fmt.Errorf("read %s: %w", dir, err)
	}
	if data, ok := res.Files["meta.yaml"]; ok {
		res.Meta, _ = parseMeta(data, false)
	}
	return res, nil
}

func loadFile(fsys fs.FS, kind, file, code string) (check.Resource, error) {
	data, err := fs.ReadFile(fsys, file)
	if err != nil {
		return check.Resource{}, fmt.Errorf("read %s: %w", file, err)
	}
	res := check.Resource{
		Ref:   resource.Ref{Kind: kind, Code: code},
		Files: map[string][]byte{path.Base(file): data},
	}
	res.Meta, _ = parseMeta(data, true)
	return res, nil
}

// parseMeta reads the metadata, from the whole file or from its "meta" key. It goes through JSON
// so that the field names are the ones resource.Meta declares.
func parseMeta(data []byte, underKey bool) (resource.Meta, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return resource.Meta{}, err
	}
	if underKey {
		m, _ := doc["meta"].(map[string]any)
		doc = m
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return resource.Meta{}, err
	}
	var meta resource.Meta
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&meta); err != nil {
		return resource.Meta{}, err
	}
	return meta, nil
}
