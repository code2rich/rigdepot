// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package repoformat

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
)

// document is one YAML file read into the JSON data model (maps with string keys, lists,
// strings, numbers, booleans and null), with the line each value starts on.
type document struct {
	value any
	lines map[string]int // JSON pointer -> line
}

// line returns the line of the value at a JSON pointer, or of the nearest value that contains
// it, or 0.
func (d document) line(ptr string) int {
	for {
		if l, ok := d.lines[ptr]; ok {
			return l
		}
		i := strings.LastIndex(ptr, "/")
		if i < 0 {
			return 0
		}
		ptr = ptr[:i]
	}
}

// yamlError is a file that is not well-formed YAML, or not in the subset these formats use.
type yamlError struct {
	line int
	msg  string
}

func (e *yamlError) Error() string { return e.msg }

// parseYAML reads one YAML document. An empty file is null.
//
// A date written without quotes (at: 2026-11-24) is a timestamp to a YAML reader; JSON has no
// such type, so the text is kept as written. A key that appears twice, a key that is not a plain
// value, more than one document and aliases are refused: none of them has a place in these
// formats, and each would make what a person sees differ from what is read.
func parseYAML(data []byte) (document, error) {
	d := document{lines: map[string]int{}}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return d, &yamlError{line: lineOf(err), msg: err.Error()}
	}
	if doc.Kind == 0 {
		return d, nil
	}
	n := &doc
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) != 1 {
			return d, &yamlError{msg: "want exactly one YAML document"}
		}
		n = doc.Content[0]
	}
	v, err := d.from(n, "")
	if err != nil {
		return d, err
	}
	d.value = v
	return d, nil
}

func (d document) from(n *yaml.Node, ptr string) (any, error) {
	d.lines[ptr] = n.Line
	switch n.Kind {
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Kind != yaml.ScalarNode {
				return nil, &yamlError{line: key.Line, msg: "a key has to be a plain value"}
			}
			if _, dup := out[key.Value]; dup {
				return nil, &yamlError{line: key.Line, msg: fmt.Sprintf("the key %q appears twice", key.Value)}
			}
			v, err := d.from(value, ptr+"/"+escapePointer(key.Value))
			if err != nil {
				return nil, err
			}
			out[key.Value] = v
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for i, item := range n.Content {
			v, err := d.from(item, ptr+"/"+strconv.Itoa(i))
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.ScalarNode:
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, &yamlError{line: n.Line, msg: err.Error()}
		}
		if _, isTime := v.(time.Time); isTime {
			return n.Value, nil
		}
		return v, nil
	}
	return nil, &yamlError{line: n.Line, msg: "aliases and anchors are not used in these formats"}
}

func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// lineOf returns the line an error from the YAML reader points at, or 0.
func lineOf(err error) int {
	var le *yaml.LoadError
	if errors.As(err, &le) {
		return le.Mark.Line
	}
	return 0
}
