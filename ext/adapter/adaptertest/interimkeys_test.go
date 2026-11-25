// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adaptertest

import (
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
)

var entryValue = map[string]any{"type": "stdio", "command": "x", "args": []any{"--a"}, "env": map[string]any{}}

// TODO(M0-20): delete this file with interimkeys.go.

// The interim key edits have to be exactly reversible, or the uninstall assertion would test the
// suite instead of the adapter.
func TestInsertJSONIsReversible(t *testing.T) {
	cases := map[string]struct {
		in     string
		format adapter.Format
		want   string // a piece the result has to contain
	}{
		"nested, existing levels":     {"{\n  \"mcp\": {\n    \"servers\": {\n      \"a\": 1\n    }\n  }\n}\n", adapter.JSON, "\n      \"k\": {\n        \"args\""},
		"missing levels":              {"{\n  \"theme\": \"x\"\n}", adapter.JSON, "\"mcp\": {\n    \"servers\": {"},
		"empty object":                {"{}", adapter.JSON, "{\n  \"mcp\""},
		"one line":                    {`{"a": 1}`, adapter.JSON, `{"a": 1, "mcp": {"servers":{"k":`},
		"comments and trailing comma": {"{\n  // c\n  \"mcp\": {\"servers\": {\n    \"a\": 1, // x\n  },},\n}\n", adapter.JSONC, ""},
		"crlf, tabs, bom":             {"\ufeff{\r\n\t\"mcp\": {}\r\n}", adapter.JSONC, "{\r\n\t\t\"servers\": {\r\n\t\t\t\"k\""},
	}
	for name, c := range cases {
		out, ins, err := insertJSON([]byte(c.in), c.format, []string{"mcp", "servers", "k"}, entryValue)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !strings.Contains(string(out), c.want) {
			t.Errorf("%s: want %q in\n%s", name, c.want, out)
		}
		doc, err := decodeJSON(out, c.format)
		if err != nil {
			t.Errorf("%s: the result does not parse:\n%s", name, out)
			continue
		}
		if v, ok := lookup(doc, []string{"mcp", "servers", "k"}); !ok || v.(map[string]any)["command"] != "x" {
			t.Errorf("%s: the key is not there:\n%s", name, out)
		}
		back, remove, err := undoInsertion(out, ins)
		if err != nil || remove || string(back) != c.in {
			t.Errorf("%s: undo gave %q, %v, %v", name, back, remove, err)
		}
	}

	if _, _, err := insertJSON([]byte(`{"mcp": {"servers": {"k": 1}}}`), adapter.JSON, []string{"mcp", "servers", "k"}, 1); err == nil {
		t.Error("an existing key was inserted again")
	}
	if _, _, err := insertJSON([]byte(`{"mcp": []}`), adapter.JSON, []string{"mcp", "servers", "k"}, 1); err == nil {
		t.Error("a key was put into a list")
	}
	if _, _, err := insertJSON([]byte(`{"mcp": `), adapter.JSON, []string{"mcp", "k"}, 1); err != errUnparsable {
		t.Errorf("a broken file gave %v", err)
	}
	out, ins, err := insertJSON(nil, adapter.JSON, []string{"mcp", "k"}, 1)
	if err != nil || string(out) != "{\n  \"mcp\": {\n    \"k\": 1\n  }\n}\n" || !ins.Created {
		t.Errorf("a new file: %q, %+v, %v", out, ins, err)
	}
	if _, _, err := undoInsertion([]byte("{}"), insertion{Offset: 1, Text: "xx"}); err == nil {
		t.Error("undo of text that is no longer there succeeded")
	}
}
