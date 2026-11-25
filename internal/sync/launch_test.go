// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/tailscale/hujson"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/mcprun"
)

// servers reads the MCP entries Fake Code has.
func (w *world) servers() map[string]map[string]any {
	w.t.Helper()
	var doc struct {
		MCP struct {
			Servers map[string]map[string]any `json:"servers"`
		} `json:"mcp"`
	}
	if raw := w.read("~/.fakecode/settings.jsonc"); raw != "" {
		std, err := hujson.Standardize([]byte(raw))
		if err != nil {
			w.t.Fatal(err)
		}
		if err := json.Unmarshal(std, &doc); err != nil {
			w.t.Fatal(err)
		}
	}
	return doc.MCP.Servers
}

// The MCP modes in which the command line starts the servers: the single entry replaces every
// server's entry with one that starts "rgd mcp serve"; direct mode keeps one entry per server,
// each starting "rgd mcp run <code>". Switching between them undoes the other's entries, and the
// single entry goes when there is no server left.
func TestLaunchModes(t *testing.T) {
	w := newWorld(t)
	r := newRepo().skill("unit-testing", "v1\n")
	r["servers/jira/server.txt"] = "jira-mcp\n"
	r["servers/jira/meta.yaml"] = "name: jira\n"
	r["servers/wiki/server.txt"] = "wiki-mcp\n"
	r["servers/wiki/meta.yaml"] = "name: wiki\n"
	const launcher = "/opt/example/bin/" + brand.CLI
	yes := ConsentInput{YesGlobal: true}

	w.run(r.state(t, desired.Filter{}), Options{MCPMode: adapter.MCPSingleEntry, Launcher: launcher}, yes)
	got := w.servers()
	if len(got) != 1 || got[brand.MCPEntry]["command"] != launcher || !slices.Equal(anyStrings(got[brand.MCPEntry]["args"]), []string{"mcp", "serve"}) {
		t.Fatalf("single entry: %v", got)
	}
	if e, ok := w.manifest().Find("fakecode:key:~/.fakecode/settings.jsonc#mcp.servers." + brand.MCPEntry); !ok || e.Resource != brand.MCPEntry {
		t.Errorf("manifest: %+v", w.manifest().Entries)
	}
	if !w.exists(unitSkill) {
		t.Error("the skill is not there")
	}

	w.run(r.state(t, desired.Filter{}), Options{MCPMode: adapter.MCPLauncher, Launcher: launcher}, yes)
	got = w.servers()
	if len(got) != 2 || got[brand.MCPEntry] != nil || !slices.Equal(anyStrings(got[brand.Prefix+"jira"]["args"]), []string{"mcp", "run", "jira"}) ||
		got[brand.Prefix+"wiki"]["command"] != launcher {
		t.Fatalf("direct: %v", got)
	}

	w.run(r.state(t, desired.Filter{}), Options{MCPMode: adapter.MCPSingleEntry, Launcher: launcher}, yes)
	if got := w.servers(); len(got) != 1 || got[brand.MCPEntry] == nil {
		t.Fatalf("back to the single entry: %v", got)
	}
	// No server left: no entry either.
	w.run(r.without("jira").without("wiki").state(t, desired.Filter{}), Options{MCPMode: adapter.MCPSingleEntry, Launcher: launcher}, yes)
	if got := w.servers(); len(got) != 0 {
		t.Errorf("without servers: %v", got)
	}
}

func anyStrings(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// connectorSpec is an MCP server's Spec as the mcp kind has it.
type connectorSpec struct{}

func (connectorSpec) Connection() kind.Connection {
	return kind.Connection{Transport: "http", URL: "https://jira.example.com/mcp",
		Headers: map[string]adapter.Value{"Authorization": {Literal: "Bearer ", Secret: "JIRA_API_TOKEN"}}}
}

// A sync keeps how to reach every MCP server, for rgd mcp run, serve and call.
func TestKeepLocalKeepsTheMCPServers(t *testing.T) {
	w := newWorld(t)
	st := &desired.State{Resources: []desired.Resource{{
		Ref: resource.Ref{Kind: resource.KindMCP, Code: "jira"}, Meta: resource.Meta{Name: "Jira"}, Digest: "sha256:1", Spec: connectorSpec{},
	}}}
	l := w.keepLocal(st, nil)
	if !slices.Equal(l.MCP.Written, []string{"jira"}) {
		t.Fatalf("mcp %+v", l.MCP)
	}
	if s, err := mcprun.Load(w.data, "jira"); err != nil || s.Headers["Authorization"].Secret != "JIRA_API_TOKEN" {
		t.Errorf("Load: %+v, %v", s, err)
	}
}
