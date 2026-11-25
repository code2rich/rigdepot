// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
)

// definition returns the document of a single-file kind (mcp/<code>.yaml) without its "meta"
// key, in the JSON data model. It is empty for directory-shaped kinds.
func definition(k kind.Kind, files fs.FS, code string) (map[string]any, error) {
	if k == nil || !k.Layout().File {
		return map[string]any{}, nil
	}
	data, err := fs.ReadFile(files, code+k.Layout().Ext)
	if err != nil {
		return nil, err
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	m, ok := jsonValue(v).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("catalog: %s%s is not a mapping", code, k.Layout().Ext)
	}
	delete(m, "meta")
	return m, nil
}

// jsonValue turns what the YAML reader returns into the JSON data model: mappings with string
// keys, lists and scalars.
func jsonValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = jsonValue(e)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[fmt.Sprint(k)] = jsonValue(e)
		}
		return m
	case []any:
		for i, e := range x {
			x[i] = jsonValue(e)
		}
		return x
	}
	return v
}

// definitionFile writes the document of a resource registered on the web: "meta" first, then
// the definition's keys in order, as YAML, the way it would be in a repository.
func definitionFile(meta resource.Meta, def map[string]any) ([]byte, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}
	add := func(key string, value any) error {
		k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
		v := &yaml.Node{}
		if err := v.Encode(value); err != nil {
			return err
		}
		root.Content = append(root.Content, k, v)
		return nil
	}
	var m map[string]any
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if err := add("meta", m); err != nil {
		return nil, err
	}
	for _, k := range slices.Sorted(maps.Keys(def)) {
		if k == "meta" {
			continue
		}
		if err := add(k, def[k]); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// LocalConfig is one setting each computer fills in for itself, from .env.example: its name and
// the comment above it. Values never reach the server.
type LocalConfig struct {
	Name        string
	Description string
}

// localConfig reads the names and descriptions of .env.example at the resource's root.
func localConfig(files fs.FS) []LocalConfig {
	data, err := fs.ReadFile(files, ".env.example")
	if err != nil {
		return nil
	}
	var out []LocalConfig
	var comment []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			comment = nil
		case strings.HasPrefix(line, "#"):
			comment = append(comment, strings.TrimSpace(strings.TrimPrefix(line, "#")))
		default:
			name, _, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
			if name = strings.TrimSpace(name); ok && name != "" {
				out = append(out, LocalConfig{Name: name, Description: strings.Join(comment, " ")})
			}
			comment = nil
		}
	}
	return out
}

var secretRef = regexp.MustCompile(`\$\{secret:([A-Z][A-Z0-9_]{1,63})\}`)

// secretNames returns the well-formed secret names a resource's files and metadata refer to.
// An upload or a web registration has no rigdepot.yaml to define them: every name it uses
// counts as defined, and which secrets exist on the server is the secrets domain's to check.
func secretNames(meta []string, contents ...[]byte) []string {
	var out []string
	for _, n := range meta {
		if resource.ValidSecretName(n) {
			out = append(out, n)
		}
	}
	for _, c := range contents {
		for _, m := range secretRef.FindAllSubmatch(c, -1) {
			out = append(out, string(m[1]))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// syntheticConfig is the rigdepot.yaml of a repository made for one uploaded or registered
// resource: the server's stages and products, and the secrets it names. It is JSON, which YAML
// reads.
func syntheticConfig(stages []resource.Stage, products, secrets []string) []byte {
	type stage struct {
		ID     string `json:"id"`
		Name   string `json:"name,omitempty"`
		Hidden bool   `json:"hidden,omitempty"`
	}
	type product struct {
		Code string `json:"code"`
	}
	cfg := struct {
		Schema   int                       `json:"schema"`
		Stages   []stage                   `json:"stages"`
		Products []product                 `json:"products,omitempty"`
		Secrets  map[string]map[string]any `json:"secrets,omitempty"`
	}{Schema: 1}
	for _, st := range stages {
		cfg.Stages = append(cfg.Stages, stage{ID: st.ID, Name: st.Name, Hidden: st.Hidden})
	}
	for _, p := range products {
		cfg.Products = append(cfg.Products, product{Code: p})
	}
	if len(secrets) > 0 {
		cfg.Secrets = map[string]map[string]any{}
		for _, n := range secrets {
			cfg.Secrets[n] = map[string]any{}
		}
	}
	return []byte(encodeJSON(cfg))
}
