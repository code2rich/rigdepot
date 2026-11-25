// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
)

// planWith plans the given servers against a config.toml with the given content (nil: no file;
// readErr: the file is there and cannot be read).
func planWith(t *testing.T, config []byte, readErr error, mode adapter.MCPMode, servers ...adapter.MCPServer) adapter.Plan {
	t.Helper()
	env := adapter.Env{
		GOOS: "linux",
		Home: "/home/example",
		ReadFile: func(p string) ([]byte, error) {
			if p != "~/.codex/config.toml" {
				t.Errorf("Plan read %s", p)
			}
			switch {
			case readErr != nil:
				return nil, readErr
			case config == nil:
				return nil, fs.ErrNotExist
			}
			return config, nil
		},
	}
	arts := make([]adapter.Artifact, len(servers))
	for i, s := range servers {
		arts[i] = s
	}
	plan, err := New().Plan(context.Background(), adapter.PlanRequest{
		Env:       env,
		Detection: adapter.Detection{Status: adapter.Installed, Roots: map[string]string{"config": "~/.codex"}},
		Artifacts: arts,
		Options:   adapter.Options{MCPMode: mode},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// entryName stands for the prefixed name the core gives an entry. The adapter takes it as it is;
// the prefix itself comes from internal/brand, which a built-in cannot import.
const entryName = "pfx-jira"

func stdio(env map[string]adapter.Value) adapter.MCPServer {
	return adapter.MCPServer{
		Resource:  resource.Ref{Kind: resource.KindMCP, Code: "jira", Version: 1},
		EntryName: entryName,
		Transport: adapter.Stdio,
		Command:   "jira-mcp",
		Env:       env,
	}
}

func httpServer(headers map[string]adapter.Value) adapter.MCPServer {
	return adapter.MCPServer{
		Resource:  resource.Ref{Kind: resource.KindMCP, Code: "jira", Version: 1},
		EntryName: entryName,
		Transport: adapter.HTTP,
		URL:       "https://jira.example.com/mcp",
		Headers:   headers,
	}
}

// outcome returns the value of the one key the plan sets, as JSON, or the one skip's reason and detail.
func outcome(t *testing.T, p adapter.Plan) string {
	t.Helper()
	if len(p.Ops)+len(p.Skips) != 1 {
		t.Fatalf("want one operation or skip, got %+v", p)
	}
	if len(p.Skips) == 1 {
		return string(p.Skips[0].Reason) + ": " + p.Skips[0].Detail
	}
	k := p.Ops[0].(adapter.SetKey)
	if k.File != "~/.codex/config.toml" || k.Format != adapter.TOML || strings.Join(k.Path, ".") != "mcp_servers."+entryName {
		t.Errorf("set-key %s %s %v", k.File, k.Format, k.Path)
	}
	b, err := json.Marshal(k.Value)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Personal secrets become names of environment variables, in the fields Codex reads (checked on
// codex-cli 0.172.0 with codex mcp list --json). What cannot be said that way is not delivered.
func TestSecrets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		server adapter.MCPServer
		mode   adapter.MCPMode
		want   string
	}{
		{"stdio, literal and forwarded variable", stdio(map[string]adapter.Value{
			"JIRA_TOKEN": {Secret: "JIRA_TOKEN"},
			"JIRA_SITE":  {Literal: "example"},
		}), adapter.MCPNativeEnv, `{"command":"jira-mcp","env":{"JIRA_SITE":"example"},"env_vars":["JIRA_TOKEN"]}`},
		{"stdio, empty mode means native-env", stdio(map[string]adapter.Value{"B": {Secret: "B"}, "A": {Secret: "A"}}), "",
			`{"command":"jira-mcp","env_vars":["A","B"]}`},
		{"stdio, variable named differently from the secret", stdio(map[string]adapter.Value{"JIRA_TOKEN": {Secret: "JIRA_API_TOKEN"}}), adapter.MCPNativeEnv,
			"needs_secret_support: the variable JIRA_TOKEN is the personal secret JIRA_API_TOKEN; Codex passes a variable on under its own name only (env_vars), so the server would not find it"},
		{"stdio, text in front of the secret", stdio(map[string]adapter.Value{"JIRA_TOKEN": {Literal: "x-", Secret: "JIRA_TOKEN"}}), adapter.MCPNativeEnv,
			"needs_secret_support: the variable JIRA_TOKEN is the personal secret JIRA_TOKEN with text in front of it; Codex can only pass a variable on unchanged"},
		{"http, bearer token", httpServer(map[string]adapter.Value{"Authorization": {Literal: "Bearer ", Secret: "JIRA_TOKEN"}}), adapter.MCPNativeEnv,
			`{"bearer_token_env_var":"JIRA_TOKEN","url":"https://jira.example.com/mcp"}`},
		{"http, header from the environment and a literal one", httpServer(map[string]adapter.Value{
			"X-Api-Key": {Secret: "JIRA_KEY"},
			"X-Team":    {Literal: "core"},
		}), adapter.MCPNativeEnv, `{"env_http_headers":{"X-Api-Key":"JIRA_KEY"},"http_headers":{"X-Team":"core"},"url":"https://jira.example.com/mcp"}`},
		{"http, whole Authorization from the environment", httpServer(map[string]adapter.Value{"Authorization": {Secret: "JIRA_AUTH"}}), adapter.MCPNativeEnv,
			`{"env_http_headers":{"Authorization":"JIRA_AUTH"},"url":"https://jira.example.com/mcp"}`},
		{"http, another scheme", httpServer(map[string]adapter.Value{"Authorization": {Literal: "Token ", Secret: "JIRA_TOKEN"}}), adapter.MCPNativeEnv,
			`needs_secret_support: the header Authorization is the personal secret JIRA_TOKEN with "Token " in front of it; Codex can only send a variable as it is, or as a bearer token`},
		{"launcher mode has no references", httpServer(map[string]adapter.Value{"Authorization": {Literal: "Bearer ", Secret: "JIRA_TOKEN"}}), adapter.MCPLauncher,
			`needs_secret_support: the header Authorization refers to the personal secret JIRA_TOKEN, and the MCP mode "launcher" has no way to deliver it`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := outcome(t, planWith(t, nil, nil, tc.mode, tc.server)); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// A config.toml that cannot be read or parsed is unknown; one whose mcp_servers cannot take a
// table of ours without rewriting the person's text is unwritable. Neither gets a key.
func TestConfigState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  string
		readErr error
		want    string
	}{
		{"no servers yet", "model = \"gpt-5.2-codex\"\n", nil, "ok"},
		{"servers with headers and inline entries", "[mcp_servers]\ndocs = { command = \"npx\" }\n\n[mcp_servers.other]\nurl = \"https://example.com\"\n", nil, "ok"},
		{"servers as dotted keys", "mcp_servers.docs.command = \"npx\"\n", nil, "ok"},
		{"an inline table under another table is not ours", "[profiles.fast]\nmcp_servers = { docs = { command = \"npx\" } }\n", nil, "ok"},
		{"byte order mark and CRLF", "\xef\xbb\xbfmodel = \"x\"\r\n[mcp_servers.docs]\r\ncommand = \"npx\"", nil, "ok"},
		{"inline table", "mcp_servers = { docs = { command = \"npx\" } }\n", nil, "config_unwritable: ~/.codex/config.toml: mcp_servers is written as an inline table"},
		{"empty inline table", "mcp_servers = {}\n", nil, "config_unwritable: ~/.codex/config.toml: mcp_servers is written as an inline table"},
		{"array of tables", "[[mcp_servers]]\ncommand = \"npx\"\n", nil, "config_unwritable: ~/.codex/config.toml: mcp_servers is written as an array of tables"},
		{"not a table", "mcp_servers = \"none\"\n", nil, "config_unwritable: ~/.codex/config.toml: mcp_servers is not a table"},
		{"does not parse", "model = \"gpt\n", nil, "config_unreadable: ~/.codex/config.toml: not valid TOML at line 1"},
		{"duplicate table", "[mcp_servers.docs]\n[mcp_servers.docs]\n", nil, "config_unreadable: ~/.codex/config.toml: not valid TOML"},
		{"cannot be read", "", fs.ErrPermission, "config_unreadable: ~/.codex/config.toml: cannot be read"},
		{"cannot be read, other error", "", errors.New("i/o error"), "config_unreadable: ~/.codex/config.toml: cannot be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := planWith(t, []byte(tc.config), tc.readErr, adapter.MCPNativeEnv, stdio(nil))
			got := outcome(t, p)
			if len(p.Ops) == 1 {
				got = "ok"
			}
			if !strings.HasPrefix(got, tc.want) {
				t.Errorf("got  %s\nwant %s…", got, tc.want)
			}
		})
	}
}

func TestCapabilities(t *testing.T) {
	caps := New().Capabilities(adapter.Detection{Status: adapter.Installed})
	want := adapter.Capabilities{Skills: true, MCP: adapter.MCPCaps{Stdio: true, HTTP: true, EnvRef: true}}
	if caps != want {
		t.Errorf("got %+v, want %+v", caps, want)
	}
}

// A name that is not a bare TOML key is a defect of the resource kind, not something about the
// person's computer: Plan fails instead of writing a quoted key.
func TestBadEntryName(t *testing.T) {
	s := stdio(nil)
	s.EntryName = entryName + ".b"
	_, err := New().Plan(context.Background(), adapter.PlanRequest{
		Env:       adapter.Env{ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist }},
		Detection: adapter.Detection{Status: adapter.Installed, Roots: map[string]string{"config": "~/.codex"}},
		Artifacts: []adapter.Artifact{s},
	})
	if err == nil {
		t.Fatal("Plan accepted an entry name with a dot")
	}
}

// The single entry (MCPSingleEntry): one [mcp_servers.<entry>] table with the command line's
// proxy as its command, in the shape of any stdio server without secrets.
func TestSingleEntry(t *testing.T) {
	single := adapter.MCPServer{Resource: resource.Ref{Kind: resource.KindMCP, Code: "jira"}, EntryName: entryName,
		Transport: adapter.Stdio, Command: "/opt/example/bin/tool", Args: []string{"mcp", "serve"}}
	if got := outcome(t, planWith(t, nil, nil, adapter.MCPSingleEntry, single)); got != `{"args":["mcp","serve"],"command":"/opt/example/bin/tool"}` {
		t.Errorf("entry: %s", got)
	}
	secret := stdio(map[string]adapter.Value{"JIRA_TOKEN": {Secret: "JIRA_TOKEN"}})
	if got := outcome(t, planWith(t, nil, nil, adapter.MCPSingleEntry, secret)); !strings.HasPrefix(got, string(adapter.NeedsSecretSupport)) {
		t.Errorf("secret: %s", got)
	}
}
