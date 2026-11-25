// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package repoformat

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	jskind "github.com/santhosh-tekuri/jsonschema/v6/kind"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/schema"
)

// newCompiler returns a compiler that knows every embedded schema under its $id, so that
// references between them resolve without fetching anything. It also returns the base address
// the $id values share.
func newCompiler() (*jsonschema.Compiler, string, error) {
	c := jsonschema.NewCompiler()
	// Nothing is ever fetched: a reference to a schema that is not embedded is an error.
	c.UseLoader(jsonschema.SchemeURLLoader{})
	base := ""
	err := fs.WalkDir(schema.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(schema.FS(), p)
		if err != nil {
			return err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		id, _ := doc.(map[string]any)["$id"].(string)
		if id == "" {
			return fmt.Errorf("%s has no $id", p)
		}
		if b, ok := strings.CutSuffix(id, p); ok {
			base = b
		}
		return c.AddResource(id, doc)
	})
	return c, base, err
}

// embedded holds the compiled schemas of rigdepot.yaml and of the metadata.
var embedded = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	c, base, err := newCompiler()
	if err != nil {
		return nil, err
	}
	out := map[string]*jsonschema.Schema{}
	for _, name := range []string{schema.Repo, schema.Meta} {
		if out[name], err = c.Compile(base + name); err != nil {
			return nil, err
		}
	}
	return out, nil
})

// compileKind compiles the schema a kind returns. It may refer to the embedded schemas by their
// $id; the metadata of a single-file kind usually does.
func compileKind(k kind.Kind) (*jsonschema.Schema, error) {
	src := k.Schema()
	if len(bytes.TrimSpace(src)) == 0 {
		return nil, nil
	}
	c, base, err := newCompiler()
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("the schema of kind %q is not JSON: %w", k.ID(), err)
	}
	id := base + "repo/v1/kinds/" + k.ID() + ".schema.json"
	if obj, ok := doc.(map[string]any); ok {
		if own, ok := obj["$id"].(string); ok && own != "" {
			id = own
		}
	}
	if err := c.AddResource(id, doc); err != nil {
		return nil, fmt.Errorf("the schema of kind %q: %w", k.ID(), err)
	}
	sch, err := c.Compile(id)
	if err != nil {
		return nil, fmt.Errorf("the schema of kind %q does not compile: %w", k.ID(), err)
	}
	return sch, nil
}

// validate checks a document against a schema and returns one issue per place it does not
// match. at is the JSON pointer of the part of the document being validated ("" for all of it).
func validate(sch *jsonschema.Schema, doc document, file, at string) []Issue {
	v := doc.value
	if at != "" {
		v = lookup(doc.value, at)
	}
	err := sch.Validate(v)
	if err == nil {
		return nil
	}
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return []Issue{{File: file, Code: CodeSchema, Message: err.Error()}}
	}
	var out []Issue
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		names, isName := e.ErrorKind.(*jskind.PropertyNames)
		if len(e.Causes) > 0 && !isName {
			for _, c := range e.Causes {
				walk(c)
			}
			return
		}
		ptr := at + pointer(e.InstanceLocation)
		msg := ""
		if isName {
			msg = fmt.Sprintf("%q is not an allowed name here", names.Property)
		} else if u := e.BasicOutput(); u.Error != nil {
			msg = u.Error.String()
		}
		out = append(out, Issue{
			File:    file,
			Line:    doc.line(ptr),
			Field:   field(ptr),
			Code:    CodeSchema,
			Message: msg,
		})
	}
	walk(verr)
	slices.SortStableFunc(out, func(a, b Issue) int { return a.Line - b.Line })
	return slices.CompactFunc(out, func(a, b Issue) bool { return a == b })
}

func pointer(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteString("/" + escapePointer(t))
	}
	return b.String()
}

// field writes a JSON pointer the way people read a field: "meta.stages.1".
func field(ptr string) string {
	parts := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
	}
	return strings.Join(parts, ".")
}

func lookup(v any, ptr string) any {
	if ptr == "" {
		return v
	}
	for _, tok := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")]
	}
	return v
}

// unknownFields reports the fields of v that sch does not describe. Fields whose name starts
// with "x-" are the author's own and are not reported. An object whose schema lists no
// properties at all is free-form, and so is one with a schema for additional properties.
//
// Schemas allow unknown fields so that an older program can read what a newer one wrote; this
// is what tells a person that a field is probably a typo.
func unknownFields(sch *jsonschema.Schema, doc document, file, at string) []Issue {
	var out []Issue
	var walk func(sch *jsonschema.Schema, v any, ptr string)
	walk = func(sch *jsonschema.Schema, v any, ptr string) {
		parts := flatten(sch)
		switch v := v.(type) {
		case map[string]any:
			if len(parts) == 0 {
				return // the schema says nothing about this object's fields
			}
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				child := ptr + "/" + escapePointer(k)
				matched, free := false, false
				for _, s := range parts {
					if p, ok := s.Properties[k]; ok {
						matched = true
						walk(p, v[k], child)
					}
					for re, p := range s.PatternProperties {
						if re.MatchString(k) {
							matched = true
							walk(p, v[k], child)
						}
					}
					if len(s.Properties) == 0 && len(s.PatternProperties) == 0 {
						free = true
					}
					if ap, ok := s.AdditionalProperties.(*jsonschema.Schema); ok {
						free = true
						if !matched {
							walk(ap, v[k], child)
						}
					}
				}
				if !matched && !free && !strings.HasPrefix(k, "x-") {
					out = append(out, Issue{
						File:    file,
						Line:    doc.line(child),
						Field:   field(child),
						Code:    CodeUnknownField,
						Message: fmt.Sprintf("unknown field %q: possibly a typo, or a field from a newer version of the format; it is ignored", k),
					})
				}
			}
		case []any:
			for _, s := range parts {
				if s.Items2020 != nil {
					for i, item := range v {
						walk(s.Items2020, item, fmt.Sprintf("%s/%d", ptr, i))
					}
				}
			}
		}
	}
	walk(sch, lookup(doc.value, at), at)
	return out
}

// flatten returns a schema together with everything that applies to the same value in all
// cases: what it refers to and the parts of an allOf.
func flatten(sch *jsonschema.Schema) []*jsonschema.Schema {
	var out []*jsonschema.Schema
	var add func(s *jsonschema.Schema, depth int)
	add = func(s *jsonschema.Schema, depth int) {
		if s == nil || depth > 16 || slices.Contains(out, s) {
			return
		}
		out = append(out, s)
		add(s.Ref, depth+1)
		for _, a := range s.AllOf {
			add(a, depth+1)
		}
	}
	add(sch, 0)
	// Only the parts that say anything about properties decide whether a field is known.
	return slices.DeleteFunc(out, func(s *jsonschema.Schema) bool {
		return s.Properties == nil && s.PatternProperties == nil && s.AdditionalProperties == nil && s.Items2020 == nil
	})
}
