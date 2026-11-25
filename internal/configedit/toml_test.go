// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package configedit

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/brand"
)

// codexServer is an MCP entry the way the Codex adapter writes one.
func codexServer() map[string]any {
	return map[string]any{
		"command": "/opt/example/bin/" + brand.CLI,
		"args":    []any{"mcp", "run", "jira"},
		"env":     map[string]any{"JIRA_URL": "https://jira.example.com"},
	}
}

var codexPath = []string{"mcp_servers", ours}

func TestTOMLSet(t *testing.T) {
	for _, name := range []string{
		"codex-user.toml",
		"codex-user-crlf-bom.toml",
		"codex-no-mcp.toml",
		"codex-no-final-newline.toml",
		"codex-trailing-blank.toml",
		"codex-inline-server.toml",
		"codex-multiline-string.toml",
		"empty.toml",
	} {
		t.Run(name, func(t *testing.T) {
			src := fixture(t, "toml/"+name)
			out, e := mustSet(t, adapter.TOML, src, codexPath, codexServer())
			golden(t, name, out)
			checkSet(t, adapter.TOML, src, out, e, codexPath, codexServer())
			if want := len(codexPath) - countExisting(t, adapter.TOML, src, codexPath); e.Created != want {
				t.Errorf("Created = %d, want %d", e.Created, want)
			}
			// What was there before is untouched: the new table is appended.
			if !bytes.HasPrefix(out, bytes.TrimRight(src, "\r\n")) {
				t.Errorf("the person's text was changed, not only appended to")
			}
		})
	}
}

func TestTOMLSetRefuses(t *testing.T) {
	tests := []struct {
		fixture string
		want    error
	}{
		{"codex-inline-table.toml", ErrInlineTable},
		{"codex-array-of-tables.toml", ErrUnwritable},
		{"codex-not-a-table.toml", ErrUnwritable},
		{"codex-broken.toml", ErrSyntax},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			src := fixture(t, "toml/"+tt.fixture)
			out, _, err := Set(adapter.TOML, src, codexPath, codexServer())
			if !errors.Is(err, tt.want) {
				t.Errorf("Set = %v, want %v", err, tt.want)
			}
			if out != nil {
				t.Errorf("Set returned bytes to write along with an error")
			}
		})
	}
	// An inline table is a case of "cannot write": callers that only know ErrUnwritable see it.
	_, _, err := Set(adapter.TOML, fixture(t, "toml/codex-inline-table.toml"), codexPath, codexServer())
	if !errors.Is(err, ErrUnwritable) {
		t.Errorf("ErrInlineTable does not wrap ErrUnwritable: %v", err)
	}
	// A table defined by an inline table one level down.
	_, _, err = Set(adapter.TOML, fixture(t, "toml/codex-inline-server.toml"), []string{"mcp_servers", "docs", "env"}, map[string]any{})
	if !errors.Is(err, ErrInlineTable) {
		t.Errorf("Set under an inline server = %v, want ErrInlineTable", err)
	}
}

func TestTOMLSetUnderDottedKeys(t *testing.T) {
	// TOML allows a [header] for a sub-table of a table that dotted keys defined; whether the
	// edit is accepted is the parser's call, and either way nothing else may change.
	src := fixture(t, "toml/codex-dotted-keys.toml")
	out, e, err := Set(adapter.TOML, src, codexPath, codexServer())
	if err != nil {
		if !errors.Is(err, ErrUnwritable) {
			t.Fatalf("Set = %v, want success or ErrUnwritable", err)
		}
		return
	}
	checkSet(t, adapter.TOML, src, out, e, codexPath, codexServer())
}

func TestTOMLSetReplacesOurTable(t *testing.T) {
	for _, name := range []string{"codex-user.toml", "codex-user-crlf-bom.toml", "codex-no-final-newline.toml", "empty.toml"} {
		t.Run(name, func(t *testing.T) {
			src := fixture(t, "toml/"+name)
			v1, e1 := mustSet(t, adapter.TOML, src, codexPath, codexServer())
			newer := codexServer()
			newer["args"] = []any{"mcp", "run", "jira", "--read-only"}
			delete(newer, "env")
			v2, e2 := mustSet(t, adapter.TOML, v1, codexPath, newer)
			if !e2.Replaced() {
				t.Errorf("second Set did not report a replacement: %+v", e2)
			}
			checkSet(t, adapter.TOML, v1, v2, e2, codexPath, newer)

			back, how, err := Revert(v2, e1)
			if err != nil || how != Semantic {
				t.Fatalf("Revert(stale) = %v, %v", how, err)
			}
			if !reflect.DeepEqual(mustDecode(t, adapter.TOML, back), mustDecode(t, adapter.TOML, src)) {
				t.Errorf("Revert(stale) did not restore the meaning of the original")
			}
		})
	}
}

func TestTOMLRevertAfterOthersEdited(t *testing.T) {
	src := fixture(t, "toml/codex-user.toml")
	out, e := mustSet(t, adapter.TOML, src, codexPath, codexServer())
	// Someone changed a line inside our table and added a table of their own after it.
	cur := bytes.Replace(out, []byte(`"mcp", "run", "jira"`), []byte(`"mcp", "run", "jira", "-v"`), 1)
	cur = append(cur, "\n[profiles.fast]\nmodel = \"example-small\"\n"...)
	back, how, err := Revert(cur, e)
	if err != nil || how != Semantic {
		t.Fatalf("Revert = %v, %v", how, err)
	}
	want := mustDecode(t, adapter.TOML, src)
	want["profiles"] = map[string]any{"fast": map[string]any{"model": "example-small"}}
	if got := mustDecode(t, adapter.TOML, back); !reflect.DeepEqual(got, want) {
		t.Errorf("after undoing:\n got  %v\n want %v", got, want)
	}
	if !bytes.HasPrefix(back, src) {
		t.Errorf("undoing changed the person's text:\n%s", lineDiff(src, back))
	}
}

func TestTOMLRevertWhenOthersAppendedAfterUs(t *testing.T) {
	src := fixture(t, "toml/codex-user-crlf-bom.toml")
	out, e := mustSet(t, adapter.TOML, src, codexPath, codexServer())
	tail := "\r\n[profiles.fast]\r\nmodel = \"example-small\"\r\n"
	back, how, err := Revert(append(out, tail...), e)
	if err != nil || how != ByteExact {
		t.Fatalf("Revert = %v, %v; want byte-exact", how, err)
	}
	if want := append(append([]byte(nil), src...), tail...); !bytes.Equal(back, want) {
		t.Errorf("undoing changed more than our bytes:\n%s", lineDiff(want, back))
	}
}

func TestTOMLRevertNothingToUndo(t *testing.T) {
	src := fixture(t, "toml/codex-no-mcp.toml")
	out, e := mustSet(t, adapter.TOML, src, codexPath, codexServer())
	removed, err := Delete(adapter.TOML, out, codexPath)
	if err != nil {
		t.Fatal(err)
	}
	back, how, err := Revert(removed, e)
	if err != nil || how != NothingToUndo || !bytes.Equal(back, removed) {
		t.Errorf("Revert = %v, %v; want the file unchanged and NothingToUndo", how, err)
	}
}

func TestTOMLDelete(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "toml", "*.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		src := fixture(t, filepath.Join("toml", filepath.Base(f)))
		doc, err := Decode(adapter.TOML, src)
		if err != nil {
			continue
		}
		for _, path := range allPaths(doc, nil) {
			if _, isTable := mustLookup(doc, path).(map[string]any); !isTable {
				continue // only tables are deleted
			}
			out, err := Delete(adapter.TOML, src, path)
			if err != nil {
				if !errors.Is(err, ErrUnwritable) {
					t.Errorf("%s: Delete(%q): %v", filepath.Base(f), path, err)
				}
				continue
			}
			want := deepCopy(doc).(map[string]any)
			removeIn(want, path)
			if got := mustDecode(t, adapter.TOML, out); !semanticEqual(adapter.TOML, got, want, path) {
				t.Errorf("%s: Delete(%q) changed more than the table:\n got  %v\n want %v", filepath.Base(f), path, got, want)
			}
		}
	}
}

func mustLookup(doc map[string]any, path []string) any {
	v, _ := lookup(doc, path)
	return v
}

func TestTOMLDeleteLayout(t *testing.T) {
	tests := []struct {
		name, src string
		path      []string
		want      string
	}{
		{"last table", "a = 1\n\n[x.y]\nb = 2\n", []string{"x", "y"}, "a = 1\n"},
		{"between tables", "a = 1\n\n[x.y]\nb = 2\n\n[z]\nc = 3\n", []string{"x", "y"}, "a = 1\n\n[z]\nc = 3\n"},
		{"with sub-tables", "[x.y]\nb = 2\n\n[x.y.env]\nK = \"v\"\n\n[z]\nc = 3\n", []string{"x", "y"}, "\n[z]\nc = 3\n"},
		{"comment before the next table stays", "[x.y]\nb = 2\n# about z\n[z]\nc = 3\n", []string{"x", "y"}, "# about z\n[z]\nc = 3\n"},
		{"CRLF", "a = 1\r\n\r\n[x.y]\r\nb = 2\r\n", []string{"x", "y"}, "a = 1\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Delete(adapter.TOML, []byte(tt.src), tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("Delete:\n got  %q\n want %q", got, tt.want)
			}
		})
	}
}

func TestTOMLDeleteRefusesWhatItCannotFind(t *testing.T) {
	tests := []struct {
		name, src string
		path      []string
		want      error
	}{
		{"missing", "a = 1\n", []string{"x"}, ErrNotFound},
		{"inline", "x = { y = { b = 1 } }\n", []string{"x", "y"}, ErrInlineTable},
		{"dotted", "x.y.b = 1\n", []string{"x", "y"}, ErrUnwritable},
		{"scattered", "[x.y]\nb = 1\n[z]\nc = 1\n[x.y.env]\nK = \"v\"\n", []string{"x", "y"}, ErrUnwritable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Delete(adapter.TOML, []byte(tt.src), tt.path); !errors.Is(err, tt.want) {
				t.Errorf("Delete = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestTOMLValues(t *testing.T) {
	value := map[string]any{
		"plain":       "abc",
		"quotes":      `say "hi" \ bye`,
		"control":     "tab\tnew\nline\x01\x7f",
		"unicode":     "中文 ✓",
		"int":         7,
		"int64":       int64(-9223372036854775808),
		"float":       0.1,
		"whole float": 3.0,
		"tiny":        1e-9,
		"huge":        1e300,
		"beyond 2^53": 1.0000000000000002e17,
		"bool":        true,
		"empty list":  []any{},
		"mixed list":  []any{"a", 1, map[string]any{"k": "v", "dotted.key": []any{}}, map[string]any{}},
		"a.b":         map[string]any{},
		"sub":         map[string]any{"deeper": map[string]any{"x": 1}},
	}
	path := []string{"tables", "with space", ours}
	out, e := mustSet(t, adapter.TOML, nil, path, value)
	golden(t, "values.toml", out)
	checkSet(t, adapter.TOML, nil, out, e, path, value)
	if v, _, _ := Get(adapter.TOML, out, append(path, "whole float")); v != int64(3) {
		t.Errorf("Get(whole float) = %#v", v)
	}
	if !strings.Contains(string(out), "whole float\" = 3.0\n") {
		t.Errorf("a float was not written as a TOML float:\n%s", out)
	}
}

func TestTOMLNaNInThePersonsFile(t *testing.T) {
	src := []byte("ratio = nan\n")
	out, e := mustSet(t, adapter.TOML, src, codexPath, codexServer())
	checkSet(t, adapter.TOML, src, out, e, codexPath, codexServer())
}
