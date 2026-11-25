// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package profileyaml

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/code2rich/rigdepot/ext/adapter"
)

// Problem is one thing wrong with a profile.
type Problem struct {
	// Field is where the problem is, as a path with dots ("verified.os.0"); "" is the profile
	// as a whole. A field that is missing is named by its own path ("mcp.files").
	Field string
	// Message says what is wrong, in English, for the person who wrote the profile.
	Message string
}

func (p Problem) String() string {
	if p.Field == "" {
		return p.Message
	}
	return p.Field + ": " + p.Message
}

// Error lists everything wrong with a profile, sorted by field.
type Error struct {
	Problems []Problem
}

func (e *Error) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = p.String()
	}
	return "profile: " + strings.Join(lines, "; ")
}

// Parse reads a profile written in YAML, checks it and returns it.
//
// The checks are the rules of schema/adapter/v1/profile.schema.json, written out in Go so that
// this package needs no JSON Schema library, plus three the schema cannot express: every root a
// path refers to is declared, the version probe's match compiles and has a group, and the date
// of the verification exists. Unknown fields are allowed, as the schema allows them. A date
// written without quotes (at: 2026-11-24) is kept as the text it is.
//
// When something is wrong the error is an *Error that lists every problem.
func Parse(data []byte) (adapter.Profile, error) {
	doc, err := decode(data)
	if err != nil {
		return adapter.Profile{}, err
	}
	var v validator
	v.profile(doc)
	if len(v.problems) > 0 {
		sort.SliceStable(v.problems, func(i, j int) bool { return v.problems[i].Field < v.problems[j].Field })
		return adapter.Profile{}, &Error{Problems: v.problems}
	}
	asJSON, err := json.Marshal(doc)
	if err != nil {
		return adapter.Profile{}, err
	}
	var p adapter.Profile
	if err := json.NewDecoder(bytes.NewReader(asJSON)).Decode(&p); err != nil {
		return adapter.Profile{}, err
	}
	return p, nil
}

// MustParse is Parse for a profile embedded in the program, where a mistake is a defect that the
// first test run has to show. It panics when the profile is not valid.
func MustParse(data []byte) adapter.Profile {
	p, err := Parse(data)
	if err != nil {
		panic(err)
	}
	return p
}

// decode reads exactly one YAML document into the JSON data model: maps with string keys,
// lists, strings, numbers, booleans and null.
func decode(data []byte) (any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, &Error{Problems: []Problem{{Message: "not valid YAML: " + err.Error()}}}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, &Error{Problems: []Problem{{Message: "more than one YAML document"}}}
	}
	n := &doc
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) != 1 {
			return nil, &Error{Problems: []Problem{{Message: "the document is empty"}}}
		}
		n = n.Content[0]
	}
	v, err := fromNode(n)
	if err != nil {
		return nil, &Error{Problems: []Problem{{Message: err.Error()}}}
	}
	return v, nil
}

func fromNode(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("line %d: a key has to be a plain value", key.Line)
			}
			if _, dup := out[key.Value]; dup {
				return nil, fmt.Errorf("line %d: the key %q appears twice", key.Line, key.Value)
			}
			v, err := fromNode(value)
			if err != nil {
				return nil, err
			}
			out[key.Value] = v
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, item := range n.Content {
			v, err := fromNode(item)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.ScalarNode:
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, fmt.Errorf("line %d: %w", n.Line, err)
		}
		if _, isTime := v.(time.Time); isTime {
			return n.Value, nil
		}
		return v, nil
	}
	return nil, fmt.Errorf("line %d: aliases and anchors are not used in profiles", n.Line)
}
