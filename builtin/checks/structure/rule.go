// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package structure

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/code2rich/rigdepot/builtin/checks/internal/skillmd"
	"github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/resource"
)

// The limits from the resource repository format.
const (
	maxFiles      = 200
	maxBytes      = 5 << 20
	maxPathBytes  = 180
	maxPathLevels = 8
)

const id = "structure"

type rule struct{}

// New returns the rule.
func New() check.Rule { return rule{} }

func (rule) ID() string               { return id }
func (rule) Severity() check.Severity { return check.MustFix }

func (rule) Applies(kind string) bool {
	return kind == resource.KindSkill || kind == resource.KindMCP
}

func (rule) Check(ctx context.Context, in check.Input) ([]check.Finding, error) {
	var r report
	if !resource.ValidCode(in.Ref.Code) {
		r.add("", 0, "code_invalid", map[string]any{"code": in.Ref.Code})
	}
	if err := checkFiles(in.Files, &r); err != nil {
		return nil, err
	}
	var err error
	switch in.Ref.Kind {
	case resource.KindSkill:
		err = checkSkill(in, &r)
	case resource.KindMCP:
		err = checkMCP(in, &r)
	}
	if err != nil {
		return nil, err
	}
	return r.found, nil
}

type report struct{ found []check.Finding }

func (r *report) add(file string, line int, problem string, params map[string]any) {
	r.found = append(r.found, check.Finding{
		Rule:     id,
		Severity: check.MustFix,
		File:     file,
		Line:     line,
		Message:  "check." + id + "." + problem,
		Params:   params,
	})
}

// checkFiles checks the limits on the number of files, their size and their paths.
func checkFiles(files fs.FS, r *report) error {
	count, total := 0, int64(0)
	byFolded := map[string]string{} // lower-cased path -> the path as written
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		folded := strings.ToLower(p)
		if other, ok := byFolded[folded]; ok {
			r.add(fileOrEmpty(d, p), 0, "case_clash", map[string]any{"other": other})
		} else {
			byFolded[folded] = p
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		count++
		total += info.Size()
		if len(p) > maxPathBytes {
			r.add(p, 0, "path_too_long", map[string]any{"bytes": len(p), "limit": maxPathBytes})
		}
		if depth := strings.Count(p, "/") + 1; depth > maxPathLevels {
			r.add(p, 0, "path_too_deep", map[string]any{"depth": depth, "limit": maxPathLevels})
		}
		return nil
	})
	if err != nil {
		return err
	}
	if count > maxFiles {
		r.add("", 0, "too_many_files", map[string]any{"count": count, "limit": maxFiles})
	}
	if total > maxBytes {
		r.add("", 0, "too_large", map[string]any{"bytes": total, "limit": maxBytes})
	}
	return nil
}

// fileOrEmpty returns the path for a file. A finding about a directory names no file, because a
// finding's file is always one the resource has.
func fileOrEmpty(d fs.DirEntry, p string) string {
	if d.IsDir() {
		return ""
	}
	return p
}

func checkSkill(in check.Input, r *report) error {
	fm, err := skillmd.Read(in.Files)
	var bad *skillmd.Error
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.add("", 0, "skill_md_missing", nil)
		return nil
	case errors.As(err, &bad):
		r.add(skillmd.File, bad.Line, "front_matter_"+bad.Problem, nil)
		return nil
	case err != nil:
		return err
	}
	switch {
	case !fm.Name.Text || strings.TrimSpace(fm.Name.Value) == "":
		r.add(skillmd.File, lineOrFirst(fm.Name.Line), "name_missing", nil)
	case fm.Name.Value != in.Ref.Code:
		r.add(skillmd.File, fm.Name.Line, "name_mismatch", map[string]any{"name": fm.Name.Value, "code": in.Ref.Code})
	}
	if !fm.Description.Text || strings.TrimSpace(fm.Description.Value) == "" {
		r.add(skillmd.File, lineOrFirst(fm.Description.Line), "description_missing", nil)
	}
	return nil
}

func lineOrFirst(line int) int {
	if line == 0 {
		return 1
	}
	return line
}

var yamlLine = regexp.MustCompile(`line (\d+)`)

var secretRef = regexp.MustCompile(`\$\{secret:`)

func checkMCP(in check.Input, r *report) error {
	file := in.Ref.Code + ".yaml"
	data, err := fs.ReadFile(in.Files, file)
	if errors.Is(err, fs.ErrNotExist) {
		r.add("", 0, "file_missing", map[string]any{"file": file})
		return nil
	}
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		line := 1
		if m := yamlLine.FindStringSubmatch(err.Error()); m != nil {
			line, _ = strconv.Atoi(m[1])
		}
		// The parser may name the line after the last one when the file ends too early.
		line = max(1, min(line, strings.Count(strings.TrimSuffix(string(data), "\n"), "\n")+1))
		r.add(file, line, "yaml_invalid", nil)
		return nil
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		r.add(file, min(1, len(data)), "yaml_invalid", nil) // an empty file has no line 1
		return nil
	}
	root := doc.Content[0]

	if key, meta := lookup(root, "meta"); meta == nil || meta.Kind != yaml.MappingNode {
		r.add(file, lineOf(key, root), "meta_missing", nil)
	} else if _, name := lookup(meta, "name"); !isText(name) {
		r.add(file, key.Line, "meta_name_missing", nil)
	}

	key, server := lookup(root, "server")
	if server == nil || server.Kind != yaml.MappingNode {
		r.add(file, lineOf(key, root), "server_missing", nil)
	} else {
		checkServer(file, key, server, r)
	}

	if key, delivery := lookup(root, "delivery"); delivery != nil {
		if v := delivery.Value; delivery.Kind != yaml.ScalarNode || (v != "native" && v != "on-demand") {
			r.add(file, key.Line, "delivery_invalid", map[string]any{"delivery": v})
		}
	}
	return nil
}

func checkServer(file string, serverKey, server *yaml.Node, r *report) {
	typeKey, typ := lookup(server, "type")
	switch {
	case !isText(typ):
		r.add(file, lineOf(typeKey, serverKey), "server_type_invalid", map[string]any{"type": ""})
	case typ.Value == "stdio":
		if _, cmd := lookup(server, "command"); !isText(cmd) {
			r.add(file, serverKey.Line, "server_command_missing", nil)
		}
	case typ.Value == "http" || typ.Value == "sse":
		if _, url := lookup(server, "url"); !isText(url) {
			r.add(file, serverKey.Line, "server_url_missing", nil)
		}
	default:
		r.add(file, typeKey.Line, "server_type_invalid", map[string]any{"type": typ.Value})
	}
	// ${secret:…} may only be in the values of env and headers.
	for i := 0; i+1 < len(server.Content); i += 2 {
		key, value := server.Content[i], server.Content[i+1]
		if key.Value == "env" || key.Value == "headers" {
			continue
		}
		walk(value, func(n *yaml.Node) {
			if n.Kind == yaml.ScalarNode && secretRef.MatchString(n.Value) {
				r.add(file, n.Line, "secret_not_allowed", map[string]any{"field": key.Value})
			}
		})
	}
}

// lookup returns the key and value nodes of a field in a mapping, or nils.
func lookup(m *yaml.Node, name string) (key, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// lineOf returns the line of the key, or of the fallback node when the key is missing.
func lineOf(key, fallback *yaml.Node) int {
	if key != nil {
		return key.Line
	}
	return max(fallback.Line, 1)
}

func isText(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.ScalarNode && strings.TrimSpace(n.Value) != "" && n.Tag != "!!null"
}

func walk(n *yaml.Node, fn func(*yaml.Node)) {
	fn(n)
	for _, c := range n.Content {
		walk(c, fn)
	}
}
