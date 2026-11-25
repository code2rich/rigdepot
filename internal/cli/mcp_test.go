// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/keyring"
)

// newFakeMCP is an MCP server over streamable HTTP with one tool, whoami, that answers with the
// Authorization header it got.
func newFakeMCP(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&m)
		if len(m.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch m.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "whoami", "description": "Says who you are.", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "auth=" + r.Header.Get("Authorization")}}}
		default:
			result = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// keepServer writes an MCP server the way a sync keeps it.
func (w *world) keepServer(t *testing.T, code, def string) {
	t.Helper()
	dir := filepath.Join(w.dataDir(), home.MCPDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, code+".json"), []byte(def), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The MCP commands fill the person's secret in from the sealed copy on this computer; a missing
// secret is a clear error, and a keychain that cannot be read stops them without a new key.
func TestMCPCommands(t *testing.T) {
	s := newSecretServer(t, "JIRA_API_TOKEN")
	mcp := newFakeMCP(t)
	w := newWorld(t)
	if r := w.runFast("login", s.srv.URL); r.code != 0 {
		t.Fatalf("login: %d %s", r.code, r.stderr)
	}
	if r := w.runIn(secretMarker+"\n", "secret", "set", "JIRA_API_TOKEN"); r.code != 0 {
		t.Fatalf("set: %d %s", r.code, r.stderr)
	}
	w.keepServer(t, "jira", `{"schema":1,"code":"jira","name":"Jira","transport":"http","url":"`+mcp.URL+`/mcp",
		"headers":{"Authorization":{"literal":"Bearer ","secret":"JIRA_API_TOKEN"}},"onDemand":true}`)
	w.keepServer(t, "figma", `{"schema":1,"code":"figma","name":"Figma","transport":"http","url":"`+mcp.URL+`/mcp",
		"headers":{"X-Figma-Token":{"secret":"FIGMA_TOKEN"}},"tools":[{"name":"get_file"}]}`)

	// One call: the server gets the secret in its header.
	r := w.runIn("", "mcp", "call", "jira", "whoami", `{}`)
	if r.code != 0 || strings.TrimSpace(r.stdout) != "auth=Bearer "+secretMarker {
		t.Fatalf("call: %d %q %s", r.code, r.stdout, r.stderr)
	}
	// Without a tool: the tools and their arguments.
	if r := w.runIn("", "mcp", "call", "jira"); r.code != 0 || !strings.Contains(r.stdout, "whoami") || !strings.Contains(r.stdout, `arguments: {"type":"object"}`) {
		t.Errorf("list: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if r := w.runIn("", "mcp", "call", "jira", "whoami", "[1]"); r.code != int(cliout.ExitUsage) {
		t.Errorf("not JSON: %d %s", r.code, r.stderr)
	}
	if r := w.runIn("", "mcp", "call", "nope", "x"); r.code != int(cliout.ExitFailure) || !strings.Contains(r.stderr, "sync") {
		t.Errorf("unknown server: %d %s", r.code, r.stderr)
	}

	// A secret that is not set: 3, which secret and how to set it.
	r = w.runIn("", "mcp", "call", "figma", "get_file")
	if r.code != int(cliout.ExitPending) || !strings.Contains(r.stderr, "FIGMA_TOKEN") || !strings.Contains(r.stderr, "secret set FIGMA_TOKEN") {
		t.Errorf("missing secret: %d %s", r.code, r.stderr)
	}
	// rgd mcp run serves the reason instead of failing to start.
	initialize := `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	r = w.runIn(initialize+"\n"+`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_file","arguments":{}}}`+"\n", "mcp", "run", "figma")
	if r.code != 0 || !strings.Contains(r.stdout, `"isError":true`) || !strings.Contains(r.stdout, "FIGMA_TOKEN") {
		t.Errorf("run without the secret: %d %s %s", r.code, r.stdout, r.stderr)
	}

	// The single entry: the native server's tools under its code; the on-demand one is not
	// there; a call gets the secret through.
	r = w.runIn(initialize+"\n"+`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n", "mcp", "serve")
	if r.code != 0 || !strings.Contains(r.stdout, `"figma__get_file"`) || strings.Contains(r.stdout, "jira__") {
		t.Errorf("serve: %d %s %s", r.code, r.stdout, r.stderr)
	}

	// Nothing of this left the value anywhere on disk in the clear.
	if fs := filesHolding(t, w.home, secretMarker); len(fs) != 0 {
		t.Errorf("files hold the value: %v", fs)
	}

	// The device key cannot be read: the call stops with 9, the key stays as it was and no new
	// one is made; once it can be read again, the same copies open.
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return
	}
	keyFile := filepath.Join(w.dataDir(), home.SecretsDir, keyring.DeviceKey)
	before, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("the device key is not in the file store: %v", err)
	}
	os.Chmod(keyFile, 0)
	r = w.runIn("", "mcp", "call", "jira", "whoami", `{}`)
	os.Chmod(keyFile, 0o600)
	if r.code != int(cliout.ExitStateUnknown) || !strings.Contains(r.stderr, "doctor") {
		t.Errorf("unreadable key: %d %s", r.code, r.stderr)
	}
	if after, _ := os.ReadFile(keyFile); string(after) != string(before) {
		t.Error("the device key changed")
	}
	if r := w.runIn("", "mcp", "call", "jira", "whoami", `{}`); r.code != 0 || !strings.Contains(r.stdout, secretMarker) {
		t.Errorf("after the key is readable again: %d %s %s", r.code, r.stdout, r.stderr)
	}
}
