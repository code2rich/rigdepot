// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package skillmd

import (
	"errors"
	"testing"

	"github.com/code2rich/rigdepot/ext/check/checktest"
)

func TestMain(m *testing.M) { checktest.Main(m) }

func TestParse(t *testing.T) {
	fm, err := Parse([]byte("\xef\xbb\xbf---\r\n# comment\r\nname: unit-testing\r\ndescription: >\r\n  Two\r\n  lines\r\ntags: [a]\r\n---\r\nbody\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if fm.Name != (Field{Value: "unit-testing", Line: 3, Text: true}) {
		t.Errorf("name = %+v", fm.Name)
	}
	if fm.Description != (Field{Value: "Two lines\n", Line: 4, Text: true}) {
		t.Errorf("description = %+v", fm.Description)
	}

	fm, err = Parse([]byte("---\nname: [a, b]\n---\n"))
	if err != nil || fm.Name.Text || fm.Name.Line != 2 || fm.Description.Line != 0 {
		t.Errorf("a list as the name: %+v, %v", fm, err)
	}
	if fm, err := Parse([]byte("---\n---\n")); err != nil || fm != (FrontMatter{}) {
		t.Errorf("an empty block: %+v, %v", fm, err)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]Error{
		"":                                     {Problem: Missing, Line: 1},
		"# Title\n---\nname: a\n---\n":         {Problem: Missing, Line: 1},
		"---\nname: a\n":                       {Problem: Unterminated, Line: 1},
		"---\nname: a\ndescription: [b\n---\n": {Problem: Invalid},
		"---\n- a\n- b\n---\n":                 {Problem: Invalid, Line: 2},
	}
	for text, want := range cases {
		_, err := Parse([]byte(text))
		var e *Error
		if !errors.As(err, &e) || e.Problem != want.Problem || (want.Line != 0 && e.Line != want.Line) || e.Line < 1 {
			t.Errorf("Parse(%q) = %v, want %s on line %d", text, err, want.Problem, want.Line)
		}
	}
}
