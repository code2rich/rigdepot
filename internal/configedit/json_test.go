// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package configedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/brand"
)

// claudeServer is an MCP entry the way the Claude Code adapter writes one.
func claudeServer() map[string]any {
	return map[string]any{
		"type":    "stdio",
		"command": "/opt/example/bin/" + brand.CLI,
		"args":    []any{"mcp", "run", "jira"},
		"env":     map[string]any{},
	}
}

// opencodeServer is an MCP entry the way the OpenCode adapter writes one.
func opencodeServer() map[string]any {
	return map[string]any{
		"type":        "local",
		"command":     []any{"/opt/example/bin/" + brand.CLI, "mcp", "run", "jira"},
		"environment": map[string]any{"JIRA_URL": "https://jira.example.com"},
	}
}

func TestJSONSet(t *testing.T) {
	claude := []string{"mcpServers", ours}
	opencode := []string{"mcp", "servers", ours}
	tests := []struct {
		fixture string
		format  adapter.Format
		path    []string
		value   any
	}{
		{"claude-user.json", adapter.JSON, claude, claudeServer()},
		{"claude-user-crlf-bom.json", adapter.JSON, claude, claudeServer()},
		{"claude-no-mcp.json", adapter.JSON, claude, claudeServer()},
		{"claude-empty-mcp.json", adapter.JSON, claude, claudeServer()},
		{"claude-no-final-newline.json", adapter.JSON, claude, claudeServer()},
		{"claude-oneline.json", adapter.JSON, claude, claudeServer()},
		{"claude-oneline-spaced.json", adapter.JSON, claude, claudeServer()},
		{"empty.json", adapter.JSON, claude, claudeServer()},
		{"claude-with-comment.json", adapter.JSONC, claude, claudeServer()},
		{"opencode-user.jsonc", adapter.JSONC, opencode, opencodeServer()},
		{"opencode-user-crlf-bom.jsonc", adapter.JSONC, opencode, opencodeServer()},
		{"opencode-no-servers.jsonc", adapter.JSONC, opencode, opencodeServer()},
		{"opencode-empty-with-comment.jsonc", adapter.JSONC, opencode, opencodeServer()},
		{"opencode-tabs.jsonc", adapter.JSONC, opencode, opencodeServer()},
		{"opencode-block-comment.jsonc", adapter.JSONC, opencode, opencodeServer()},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			src := fixture(t, "json/"+tt.fixture)
			out, e := mustSet(t, tt.format, src, tt.path, tt.value)
			golden(t, tt.fixture, out)
			checkSet(t, tt.format, src, out, e, tt.path, tt.value)
			if want := len(tt.path) - countExisting(t, tt.format, src, tt.path); e.Created != want {
				t.Errorf("Created = %d, want %d", e.Created, want)
			}
		})
	}
}

func countExisting(t *testing.T, format adapter.Format, src []byte, path []string) int {
	doc := mustDecode(t, format, src)
	for i := 1; i <= len(path); i++ {
		if _, ok := lookup(doc, path[:i]); !ok {
			return i - 1
		}
	}
	return len(path)
}

func TestJSONSetKeyWithDotsAndSlashes(t *testing.T) {
	src := fixture(t, "json/claude-user.json")
	path := []string{"projects", "/home/example/work/app", "mcpServers", ours}
	out, e := mustSet(t, adapter.JSON, src, path, claudeServer())
	golden(t, "claude-user-project.json", out)
	checkSet(t, adapter.JSON, src, out, e, path, claudeServer())
}

func TestJSONSetRefuses(t *testing.T) {
	tests := []struct {
		fixture string
		format  adapter.Format
		path    []string
		want    error
	}{
		{"claude-truncated.json", adapter.JSON, []string{"mcpServers", ours}, ErrSyntax},
		{"claude-with-comment.json", adapter.JSON, []string{"mcpServers", ours}, ErrSyntax},
		{"opencode-broken.jsonc", adapter.JSONC, []string{"mcp", "servers", ours}, ErrSyntax},
		{"claude-mcp-not-object.json", adapter.JSON, []string{"mcpServers", ours}, ErrUnwritable},
		{"claude-duplicate-key.json", adapter.JSON, []string{"mcpServers", ours}, ErrUnwritable},
		{"claude-user.json", adapter.JSON, []string{"numStartups", ours}, ErrUnwritable},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			src := fixture(t, "json/"+tt.fixture)
			out, _, err := Set(tt.format, src, tt.path, claudeServer())
			if !errors.Is(err, tt.want) {
				t.Errorf("Set = %v, want %v", err, tt.want)
			}
			if out != nil {
				t.Errorf("Set returned bytes to write along with an error")
			}
		})
	}
	for _, top := range []string{`[]`, `"x"`, `null`, ` `} {
		if _, _, err := Set(adapter.JSONC, []byte(top), []string{"a"}, "x"); !errors.Is(err, ErrSyntax) {
			t.Errorf("Set on %q = %v, want ErrSyntax", top, err)
		}
	}
}

func TestJSONSetReplacesOurValue(t *testing.T) {
	for _, name := range []string{"claude-user.json", "claude-user-crlf-bom.json", "claude-oneline.json"} {
		t.Run(name, func(t *testing.T) {
			src := fixture(t, "json/"+name)
			path := []string{"mcpServers", ours}
			v1, e1 := mustSet(t, adapter.JSON, src, path, claudeServer())
			newer := claudeServer()
			newer["args"] = []any{"mcp", "run", "jira", "--read-only"}
			v2, e2 := mustSet(t, adapter.JSON, v1, path, newer)
			if !e2.Replaced() {
				t.Errorf("second Set did not report a replacement: %+v", e2)
			}
			checkSet(t, adapter.JSON, v1, v2, e2, path, newer)

			// The first record no longer matches the bytes: undoing it removes the key by meaning.
			back, how, err := Revert(v2, e1)
			if err != nil || how != Semantic {
				t.Fatalf("Revert(stale) = %v, %v", how, err)
			}
			if !reflect.DeepEqual(mustDecode(t, adapter.JSON, back), mustDecode(t, adapter.JSON, src)) {
				t.Errorf("Revert(stale) did not restore the meaning of the original")
			}
		})
	}
}

func TestJSONRevertReplacementAfterRewrite(t *testing.T) {
	src := fixture(t, "json/claude-user.json")
	path := []string{"mcpServers", "docs"} // replacing a value is reversible too
	out, e := mustSet(t, adapter.JSON, src, path, map[string]any{"type": "http", "url": "https://mcp.example.com/v2"})
	rewritten := rewriteLikeATool(t, out)
	back, how, err := Revert(rewritten, e)
	if err != nil || how != Semantic {
		t.Fatalf("Revert = %v, %v", how, err)
	}
	want := mustDecode(t, adapter.JSON, src)
	want["numStartups"] = int64(43)
	if got := mustDecode(t, adapter.JSON, back); !reflect.DeepEqual(got, want) {
		t.Errorf("the previous value was not restored:\n got  %v\n want %v", got, want)
	}
}

// rewriteLikeATool re-serialises a JSON file the way Claude Code does when it saves its state:
// different key order, different layout.
func rewriteLikeATool(t *testing.T, b []byte) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(bytes.TrimPrefix(b, utf8BOM), &doc); err != nil {
		t.Fatal(err)
	}
	doc["numStartups"] = 43
	out, err := json.MarshalIndent(doc, "", "    ")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestJSONRevertAfterRewrite(t *testing.T) {
	for _, name := range []string{"claude-user.json", "claude-no-mcp.json", "claude-empty-mcp.json"} {
		t.Run(name, func(t *testing.T) {
			src := fixture(t, "json/"+name)
			path := []string{"mcpServers", ours}
			out, e := mustSet(t, adapter.JSON, src, path, claudeServer())
			rewritten := rewriteLikeATool(t, out)
			back, how, err := Revert(rewritten, e)
			if err != nil || how != Semantic {
				t.Fatalf("Revert = %v, %v", how, err)
			}
			want := mustDecode(t, adapter.JSON, src)
			want["numStartups"] = int64(43)
			if got := mustDecode(t, adapter.JSON, back); !reflect.DeepEqual(got, want) {
				t.Errorf("after undoing:\n got  %v\n want %v", got, want)
			}
		})
	}
}

func TestJSONRevertKeepsWhatOthersAddedToACreatedParent(t *testing.T) {
	src := fixture(t, "json/claude-no-mcp.json")
	out, e := mustSet(t, adapter.JSON, src, []string{"mcpServers", ours}, claudeServer())
	// The person adds a server of their own next to ours.
	theirs, _ := mustSet(t, adapter.JSON, out, []string{"mcpServers", "docs"}, map[string]any{"type": "http", "url": "https://mcp.example.com/docs"})
	back, how, err := Revert(theirs, e)
	if err != nil {
		t.Fatal(err)
	}
	doc := mustDecode(t, adapter.JSON, back)
	servers, ok := doc["mcpServers"].(map[string]any)
	if !ok || len(servers) != 1 || servers["docs"] == nil {
		t.Errorf("after undoing (%v), mcpServers = %v; want only the person's server", how, doc["mcpServers"])
	}
}

func TestJSONRevertWhenEarlierBytesChangedLength(t *testing.T) {
	src := fixture(t, "json/claude-user-crlf-bom.json")
	out, e := mustSet(t, adapter.JSON, src, []string{"mcpServers", ours}, claudeServer())
	// The tool bumped a counter near the top of the file: our bytes moved, unchanged.
	cur := bytes.Replace(out, []byte(`"numStartups": 42`), []byte(`"numStartups": 1042`), 1)
	back, how, err := Revert(cur, e)
	if err != nil || how != ByteExact {
		t.Fatalf("Revert = %v, %v; want byte-exact", how, err)
	}
	want := bytes.Replace(src, []byte(`"numStartups": 42`), []byte(`"numStartups": 1042`), 1)
	if !bytes.Equal(back, want) {
		t.Errorf("undoing changed more than our bytes:\n%s", lineDiff(want, back))
	}
}

func TestJSONRevertNothingToUndo(t *testing.T) {
	src := fixture(t, "json/claude-user.json")
	path := []string{"mcpServers", ours}
	out, e := mustSet(t, adapter.JSON, src, path, claudeServer())
	removed, err := Delete(adapter.JSON, out, path)
	if err != nil {
		t.Fatal(err)
	}
	back, how, err := Revert(removed, e)
	if err != nil || how != NothingToUndo || !bytes.Equal(back, removed) {
		t.Errorf("Revert = %v, %v; want the file unchanged and NothingToUndo", how, err)
	}
}

func TestJSONRevertRefusesAFileThatDoesNotParse(t *testing.T) {
	src := fixture(t, "json/claude-user.json")
	out, e := mustSet(t, adapter.JSON, src, []string{"mcpServers", ours}, claudeServer())
	broken := out[:len(out)/2]
	if _, _, err := Revert(broken, e); !errors.Is(err, ErrSyntax) {
		t.Errorf("Revert on a truncated file = %v, want ErrSyntax", err)
	}
}

func TestJSONDelete(t *testing.T) {
	// Deleting each of a file's members by meaning, from every fixture that parses.
	files, err := filepath.Glob(filepath.Join("testdata", "json", "*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		format := adapter.JSONC
		if strings.HasSuffix(f, ".json") {
			format = adapter.JSON
		}
		src := fixture(t, filepath.Join("json", filepath.Base(f)))
		doc, err := Decode(format, src)
		if err != nil {
			continue
		}
		for _, path := range allPaths(doc, nil) {
			out, err := Delete(format, src, path)
			if err != nil {
				if errors.Is(err, ErrUnwritable) { // a duplicated key
					continue
				}
				t.Errorf("%s: Delete(%q): %v", filepath.Base(f), path, err)
				continue
			}
			want := deepCopy(doc).(map[string]any)
			removeIn(want, path)
			if got := mustDecode(t, format, out); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: Delete(%q) changed more than the key", filepath.Base(f), path)
			}
		}
	}
}

// allPaths lists the path of every key in doc.
func allPaths(doc map[string]any, prefix []string) [][]string {
	var out [][]string
	for _, k := range sortedKeys(doc) {
		p := append(append([]string(nil), prefix...), k)
		out = append(out, p)
		if m, ok := doc[k].(map[string]any); ok {
			out = append(out, allPaths(m, p)...)
		}
	}
	return out
}

func TestJSONDeleteLayout(t *testing.T) {
	tests := []struct {
		name, src string
		path      []string
		want      string
	}{
		{"middle member", "{\n  \"a\": 1,\n  \"k\": 2,\n  \"b\": 3\n}\n", []string{"k"}, "{\n  \"a\": 1,\n  \"b\": 3\n}\n"},
		{"last member", "{\n  \"a\": 1,\n  \"k\": 2\n}\n", []string{"k"}, "{\n  \"a\": 1\n}\n"},
		{"last member, CRLF", "{\r\n  \"a\": 1,\r\n  \"k\": 2\r\n}\r\n", []string{"k"}, "{\r\n  \"a\": 1\r\n}\r\n"},
		{"only member", "{\n  \"k\": 2\n}\n", []string{"k"}, "{}\n"},
		{"one line", `{"a":1, "k":2, "b":3}`, []string{"k"}, `{"a":1, "b":3}`},
		{"one line, last", `{"a":1, "k":2}`, []string{"k"}, `{"a":1}`},
		{"trailing comma", "{\n  \"a\": 1,\n  \"k\": 2,\n}", []string{"k"}, "{\n  \"a\": 1,\n}"},
		{"comment on previous line kept", "{\n  \"a\": 1, // about a\n  \"k\": 2\n}", []string{"k"}, "{\n  \"a\": 1 // about a\n}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Delete(adapter.JSONC, []byte(tt.src), tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("Delete:\n got  %q\n want %q", got, tt.want)
			}
		})
	}
}

func TestJSONDeleteNotFound(t *testing.T) {
	src := fixture(t, "json/claude-user.json")
	for _, path := range [][]string{{"mcpServers", ours}, {"nothing", "here"}, {"numStartups", "x"}} {
		if _, err := Delete(adapter.JSON, src, path); !errors.Is(err, ErrNotFound) {
			t.Errorf("Delete(%q) = %v, want ErrNotFound", path, err)
		}
	}
}

func TestJSONGet(t *testing.T) {
	src := fixture(t, "json/opencode-user-crlf-bom.jsonc")
	v, ok, err := Get(adapter.JSONC, src, []string{"mcp", "servers", "local-notes", "disabled"})
	if err != nil || !ok || v != true {
		t.Errorf("Get = %v, %v, %v", v, ok, err)
	}
	if _, ok, err := Get(adapter.JSONC, src, []string{"mcp", "servers", ours}); ok || err != nil {
		t.Errorf("Get of a missing key = %v, %v", ok, err)
	}
	if _, _, err := Get(adapter.JSONC, fixture(t, "json/opencode-broken.jsonc"), []string{"mcp"}); !errors.Is(err, ErrSyntax) {
		t.Errorf("Get on a broken file = %v, want ErrSyntax", err)
	}
}

func TestJSONNumbers(t *testing.T) {
	value := map[string]any{
		"int": 7, "int64": int64(-9007199254740993), "float": 0.25, "whole float": 3.0, "big": 1e300,
		// Integral, beyond 2^53: its shortest spelling 100000000000000020 is a different integer.
		"beyond 2^53": 1.0000000000000002e17,
	}
	src := []byte("{\n  \"limit\": 1.50,\n  \"huge\": 1e400\n}\n")
	out, e := mustSet(t, adapter.JSON, src, []string{"x"}, value)
	checkSet(t, adapter.JSON, src, out, e, []string{"x"}, value)
	if !bytes.Contains(out, []byte(`-9007199254740993`)) {
		t.Errorf("an int64 beyond 2^53 lost precision:\n%s", out)
	}
	if !bytes.Contains(out, []byte(`100000000000000016`)) {
		t.Errorf("an integral float was not spelled out exactly:\n%s", out)
	}
	if !bytes.Contains(out, []byte(`"limit": 1.50`)) {
		t.Errorf("the person's number was respelled:\n%s", out)
	}
}

func TestJSONStringsAreNotHTMLEscaped(t *testing.T) {
	out, _ := mustSet(t, adapter.JSON, nil, []string{"x"}, "a<b>&c")
	if !bytes.Contains(out, []byte(`"a<b>&c"`)) {
		t.Errorf("got %s", out)
	}
}
