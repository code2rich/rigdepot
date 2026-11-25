// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package skillmd reads the front matter of a SKILL.md for the check rules: the fields with the
// lines they are on, so that findings can point at them.
package skillmd

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"

	"go.yaml.in/yaml/v4"
)

// File is the name of the file every skill has.
const File = "SKILL.md"

// Field is one field of the front matter.
type Field struct {
	// Value is the field's text. Empty when the field is missing or not plain text.
	Value string
	// Line is the line of SKILL.md the field is on, counted from 1. Zero when it is missing.
	Line int
	// Text reports whether the field is plain text (a YAML scalar), not a list or a mapping.
	Text bool
}

// FrontMatter is the YAML block at the top of a SKILL.md.
type FrontMatter struct {
	Name        Field
	Description Field
}

// Error says why the front matter could not be read, and on which line.
type Error struct {
	// Problem is one of the Problem constants.
	Problem string
	// Line is the line of SKILL.md the problem is on.
	Line int
	err  error
}

// The problems an Error can have.
const (
	Missing      = "missing"      // the file does not start with a line "---"
	Unterminated = "unterminated" // no closing line "---"
	Invalid      = "invalid"      // the block is not a YAML mapping
)

func (e *Error) Error() string {
	if e.err != nil {
		return fmt.Sprintf("SKILL.md line %d: front matter %s: %v", e.Line, e.Problem, e.err)
	}
	return fmt.Sprintf("SKILL.md line %d: front matter %s", e.Line, e.Problem)
}

// Read reads and parses the front matter of the SKILL.md in files. Errors from the file system
// are returned as they are, so a missing file is fs.ErrNotExist; a front matter that cannot be
// read is an *Error.
func Read(files fs.FS) (FrontMatter, error) {
	data, err := fs.ReadFile(files, File)
	if err != nil {
		return FrontMatter{}, err
	}
	return Parse(data)
}

var yamlLine = regexp.MustCompile(`line (\d+)`)

// Parse parses the front matter at the top of a SKILL.md: the block between a first line "---"
// and the next line "---". A byte order mark and CRLF line endings are allowed.
func Parse(data []byte) (FrontMatter, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(data) == 0 || string(bytes.TrimRight(lines[0], "\r\n")) != "---" {
		return FrontMatter{}, &Error{Problem: Missing, Line: 1}
	}
	var block []byte
	closed := false
	for _, l := range lines[1:] {
		if string(bytes.TrimRight(l, "\r\n")) == "---" {
			closed = true
			break
		}
		block = append(block, l...)
	}
	if !closed {
		return FrontMatter{}, &Error{Problem: Unterminated, Line: 1}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(block, &doc); err != nil {
		line := 1
		if m := yamlLine.FindStringSubmatch(err.Error()); m != nil {
			n, _ := strconv.Atoi(m[1])
			line = n + 1
		}
		return FrontMatter{}, &Error{Problem: Invalid, Line: line, err: err}
	}
	if len(doc.Content) == 0 {
		return FrontMatter{}, nil // an empty block: every field is missing
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return FrontMatter{}, &Error{Problem: Invalid, Line: root.Line + 1, err: errors.New("not a mapping")}
	}
	var fm FrontMatter
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		f := Field{Line: key.Line + 1, Text: value.Kind == yaml.ScalarNode}
		if f.Text {
			f.Value = value.Value
		}
		switch key.Value {
		case "name":
			fm.Name = f
		case "description":
			fm.Description = f
		}
	}
	return fm, nil
}
