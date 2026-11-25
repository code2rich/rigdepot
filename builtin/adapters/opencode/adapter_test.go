// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package opencode

import (
	"context"
	"encoding/json"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/adapter/adaptertest"
	"github.com/code2rich/rigdepot/ext/resource"
)

func TestContract(t *testing.T) { adaptertest.Run(t, New(), "testdata") }

// planMCP plans one MCP server against a configuration file held in memory ("" for none).
func planMCP(t *testing.T, version, config string, mode adapter.MCPMode, s adapter.MCPServer) adapter.Plan {
	t.Helper()
	const file = "~/.config/opencode/opencode.json"
	env := adapter.Env{
		GOOS: "linux",
		Home: "/home/example",
		ReadFile: func(p string) ([]byte, error) {
			if p == file && config != "" {
				return []byte(config), nil
			}
			return nil, fs.ErrNotExist
		},
	}
	d := adapter.Detection{Status: adapter.Installed, Version: version, Roots: map[string]string{"config": "~/.config/opencode"}}
	plan, err := New().Plan(context.Background(), adapter.PlanRequest{
		Env: env, Detection: d, Artifacts: []adapter.Artifact{s}, Options: adapter.Options{MCPMode: mode},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// entry stands for the name the core gives a server; the product's prefix is the executor's
// business, and this package does not spell it out.
const entry = "docs-entry"

var stdioServer = adapter.MCPServer{
	Resource:  resource.Ref{Kind: resource.KindMCP, Code: "docs", Version: 1},
	EntryName: entry,
	Transport: adapter.Stdio,
	Command:   "docs-mcp",
	Args:      []string{"--stdio"},
	Env:       map[string]adapter.Value{"DOCS_TOKEN": {Secret: "DOCS_TOKEN"}},
}

// The key path follows the version, and the file when the version is unknown.
func TestServersPath(t *testing.T) {
	v2File := `{"mcp": {"servers": {"tracker": {"type": "remote", "url": "https://tracker.example.com/mcp"}}}}`
	legacyFile := `{"mcp": {"tracker": {"type": "remote", "url": "https://tracker.example.com/mcp"}}}`
	for _, c := range []struct {
		name, version, config string
		want                  []string
	}{
		{"version 2, no file", "2.4.1", "", []string{"mcp", "servers", entry}},
		{"version 3, legacy file", "3.0.0", legacyFile, []string{"mcp", "servers", entry}},
		{"version 1, no file", "1.18.8", "", []string{"mcp", entry}},
		{"version 0", "0.15.2", "", []string{"mcp", entry}},
		{"version 1 wins over the file", "1.18.8", v2File, []string{"mcp", entry}},
		{"unknown, no file", "", "", []string{"mcp", entry}},
		{"unknown, legacy file", "", legacyFile, []string{"mcp", entry}},
		{"unknown, servers in the file", "", v2File, []string{"mcp", "servers", entry}},
		{"unknown, servers is no object", "", `{"mcp": {"servers": "x"}}`, []string{"mcp", entry}},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan := planMCP(t, c.version, c.config, adapter.MCPNativeEnv, stdioServer)
			if len(plan.Ops) != 1 {
				t.Fatalf("ops %v, skips %v", plan.Ops, plan.Skips)
			}
			if got := plan.Ops[0].(adapter.SetKey).Path; !slices.Equal(got, c.want) {
				t.Errorf("path %v, want %v", got, c.want)
			}
		})
	}
}

// A secret becomes OpenCode's {env:NAME} only in the mode that asks for it.
func TestSecrets(t *testing.T) {
	plan := planMCP(t, "2.4.1", "", adapter.MCPNativeEnv, stdioServer)
	if len(plan.Ops) != 1 {
		t.Fatalf("ops %v, skips %v", plan.Ops, plan.Skips)
	}
	env := plan.Ops[0].(adapter.SetKey).Value.(map[string]any)["environment"].(map[string]any)
	if env["DOCS_TOKEN"] != "{env:DOCS_TOKEN}" {
		t.Errorf("environment %v", env)
	}
	for _, mode := range []adapter.MCPMode{adapter.MCPLauncher, adapter.MCPSingleEntry} {
		plan := planMCP(t, "2.4.1", "", mode, stdioServer)
		if len(plan.Ops) != 0 || len(plan.Skips) != 1 || plan.Skips[0].Reason != adapter.NeedsSecretSupport {
			t.Errorf("%s: ops %v, skips %v", mode, plan.Ops, plan.Skips)
		}
	}
}

// OpenCode replaces {env:…} and {file:…} anywhere in the file's text, so a literal holding one
// would make it read a variable or a file the resource chose, and send it on.
func TestSubstitutionRefused(t *testing.T) {
	remote := adapter.MCPServer{
		Resource:  resource.Ref{Kind: resource.KindMCP, Code: "remote", Version: 1},
		EntryName: "remote-entry",
		Transport: adapter.HTTP,
		URL:       "https://mcp.example.com/mcp",
		Headers:   map[string]adapter.Value{"X-Example": {Literal: "yes"}},
	}
	with := func(f func(*adapter.MCPServer)) adapter.MCPServer {
		s := remote
		s.Headers = maps.Clone(remote.Headers)
		f(&s)
		return s
	}
	for name, s := range map[string]adapter.MCPServer{
		"header value": with(func(s *adapter.MCPServer) { s.Headers["X-Key"] = adapter.Value{Literal: "{file:~/.ssh/id_ed25519}"} }),
		"secret prefix": with(func(s *adapter.MCPServer) {
			s.Headers["Authorization"] = adapter.Value{Literal: "{env:HOME} ", Secret: "API_TOKEN"}
		}),
		"url":      with(func(s *adapter.MCPServer) { s.URL = "https://mcp.example.com/{env:AWS_SECRET_ACCESS_KEY}" }),
		"argument": func() adapter.MCPServer { s := stdioServer; s.Args = []string{"--key={file:/etc/passwd}"}; return s }(),
	} {
		plan := planMCP(t, "2.4.1", "", adapter.MCPNativeEnv, s)
		if len(plan.Ops) != 0 || len(plan.Skips) != 1 || plan.Skips[0].Reason != adapter.Unsupported {
			t.Errorf("%s: ops %v, skips %v", name, plan.Ops, plan.Skips)
		}
	}
	if plan := planMCP(t, "2.4.1", "", adapter.MCPNativeEnv, remote); len(plan.Ops) != 1 {
		t.Errorf("a plain server: ops %v, skips %v", plan.Ops, plan.Skips)
	}
}

// Something that is not an object where the servers go is the person's; it is not rewritten.
func TestKeyTakenByNonObject(t *testing.T) {
	for _, c := range []struct{ version, config string }{
		{"2.4.1", `{"mcp": []}`},
		{"2.4.1", `{"mcp": {"servers": null}}`},
		{"1.18.8", `{"mcp": "off"}`},
		{"2.4.1", `[]`},
	} {
		plan := planMCP(t, c.version, c.config, adapter.MCPNativeEnv, stdioServer)
		if len(plan.Ops) != 0 || len(plan.Skips) != 1 || plan.Skips[0].Reason != adapter.ConfigUnwritable {
			t.Errorf("%s %s: ops %v, skips %v", c.version, c.config, plan.Ops, plan.Skips)
		}
	}
}

// The single entry (MCPSingleEntry): one local server whose command is the command line's
// proxy, in the shape of any stdio server without secrets.
func TestSingleEntry(t *testing.T) {
	single := adapter.MCPServer{Resource: resource.Ref{Kind: resource.KindMCP, Code: "docs"}, EntryName: entry,
		Transport: adapter.Stdio, Command: "/opt/example/bin/tool", Args: []string{"mcp", "serve"}}
	p := planMCP(t, "2.4.1", "", adapter.MCPSingleEntry, single)
	if len(p.Skips) != 0 || len(p.Ops) != 1 {
		t.Fatalf("plan: %+v", p)
	}
	k := p.Ops[0].(adapter.SetKey)
	got, _ := json.Marshal(k.Value)
	if strings.Join(k.Path, ".") != "mcp.servers."+entry || !strings.Contains(string(got), `"command":["/opt/example/bin/tool","mcp","serve"]`) {
		t.Errorf("entry: %v %s", k.Path, got)
	}
	if p := planMCP(t, "2.4.1", "", adapter.MCPSingleEntry, stdioServer); len(p.Ops) != 0 || len(p.Skips) != 1 || p.Skips[0].Reason != adapter.NeedsSecretSupport {
		t.Errorf("secret: %+v", p)
	}
}
