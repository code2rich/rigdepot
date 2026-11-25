// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package e2e

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tailscale/hujson"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// e2eSecret is the personal secret of TestPersonalSecretFromBrowserToMCPServer. It may reach
// the MCP server, in its request header, and nothing else.
const e2eSecret = "Pq9-e2e-personal-secret-marker-3Wd8"

// fakeMCPServer is an MCP server over streamable HTTP with one tool, whoami, which answers with
// the Authorization header it got.
func fakeMCPServer(t *testing.T) *httptest.Server {
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
		var result any = map[string]any{}
		switch m.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "whoami", "description": "Says who you are.", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "auth=" + r.Header.Get("Authorization")}}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPersonalSecretFromBrowserToMCPServer is M1-28, M1-29 and M1-2A end to end: a person sets a
// secret in the browser; rgd sync writes the single entry into the tools and fetches the secret
// sealed to this computer; the MCP server gets it in its request header through rgd mcp call
// and rgd mcp serve; no file in the home directory holds it, before or after rgd logout.
func TestPersonalSecretFromBrowserToMCPServer(t *testing.T) {
	w := newWorld(t)
	before := snapshot(t, w.home)
	mcp := fakeMCPServer(t)
	srv := startServer(t, filepath.Join(testenv.Current(t).Home, t.Name()))
	b := srv.firstAdmin(t)

	def := map[string]any{"kind": "mcp", "meta": map[string]any{"name": "Probe", "when": "To check the secret"},
		"definition": map[string]any{"server": map[string]any{"type": "http", "url": mcp.URL + "/mcp",
			"headers": map[string]any{"Authorization": "Bearer ${secret:PROBE_TOKEN}"}}, "delivery": "native"}}
	raw, _ := json.Marshal(def)
	status, out := b.do(http.MethodPut, "/api/v1/resources/probe/draft", string(raw), nil)
	var draft struct{ Etag string }
	if status != http.StatusCreated || json.Unmarshal(out, &draft) != nil {
		t.Fatalf("register: %d %s", status, out)
	}
	if status, out := b.do(http.MethodPost, "/api/v1/resources/probe/publish", `{}`, map[string]string{"If-Match": draft.Etag}); status != http.StatusCreated {
		t.Fatalf("publish: %d %s", status, out)
	}
	if status, out := b.do(http.MethodPut, "/api/v1/me/secrets/PROBE_TOKEN", `{"value":"`+e2eSecret+`"}`, nil); status != http.StatusOK || strings.Contains(string(out), e2eSecret) {
		t.Fatalf("set the secret: %d %s", status, out)
	}

	w.login(srv, b)
	w.must(exitOK, "sync", "--yes-global")

	// The single entry, and nothing of the secret, in Claude Code's configuration.
	std, err := hujson.Standardize(w.read(".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var claude struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
		} `json:"mcpServers"`
	}
	json.Unmarshal(std, &claude)
	entry, ok := claude.MCPServers[brand.MCPEntry]
	if !ok || strings.Join(entry.Args, " ") != "mcp serve" || !strings.HasSuffix(filepath.ToSlash(entry.Command), brand.CLI+exeSuffix()) {
		t.Errorf("the single entry: %+v", claude.MCPServers)
	}
	if _, ok := claude.MCPServers[brand.Prefix+"probe"]; ok {
		t.Error("an entry of its own besides the single entry")
	}

	// The MCP server gets the secret, through one call and through the single entry.
	if r := w.must(exitOK, "mcp", "call", "probe", "whoami", "{}"); strings.TrimSpace(r.stdout) != "auth=Bearer "+e2eSecret {
		t.Errorf("rgd mcp call: %q", r.stdout)
	}
	cmd := exec.Command(w.rgd, "mcp", "serve")
	cmd.Env, cmd.Dir = w.env, w.home
	cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"e2e","version":"1"}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"probe__whoami","arguments":{}}}` + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil || !strings.Contains(stdout.String(), "auth=Bearer "+e2eSecret) {
		t.Errorf("rgd mcp serve: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}

	if found := holding(t, w.home, e2eSecret); len(found) != 0 {
		t.Errorf("files in the home directory hold the secret: %v", found)
	}
	vault := filepath.Join(w.home, brand.Home, "secrets", "vault.json")
	if _, err := os.Stat(vault); err != nil {
		t.Fatalf("no sealed copy: %v", err)
	}
	w.must(exitOK, "logout")
	if _, err := os.Stat(vault); err == nil {
		t.Error("the sealed copies are still there after logout")
	}
	w.must(exitOK, "uninstall")
	if d := diffTrees(before, snapshot(t, w.home).without(dataDir)); d != "" {
		t.Fatalf("after uninstall the home directory is not what it was:\n%s", d)
	}
}

func exeSuffix() string {
	if strings.HasSuffix(os.Args[0], ".exe") {
		return ".exe"
	}
	return ""
}

// holding returns the files below dir that hold s.
func holding(t *testing.T, dir, s string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, err := os.ReadFile(p); err == nil && bytes.Contains(data, []byte(s)) {
				out = append(out, p)
			}
		}
		return nil
	})
	return out
}
