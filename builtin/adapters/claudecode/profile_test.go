// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package claudecode

import (
	"slices"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/adapter/adaptertest"
	"github.com/code2rich/rigdepot/ext/adapter/profileyaml"
)

func TestMain(m *testing.M) { adaptertest.Main(m) }

// The places checked on Claude Code 2.1.330 (docs/architecture/04a-platforms.md).
func TestProfile(t *testing.T) {
	p, err := profileyaml.Parse(profileYAML)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "claude-code" || p.Verified == nil {
		t.Fatalf("id %q, verified %v", p.ID, p.Verified)
	}
	if r := p.Roots["config"]; r.Env != "CLAUDE_CONFIG_DIR" || r.Default != "~/.claude" {
		t.Errorf("config root: %+v", r)
	}
	if p.Skills == nil || p.Skills.Dir != "{config}/skills" {
		t.Errorf("skills: %+v", p.Skills)
	}
	if p.Commands == nil || p.Commands.Dir != "{config}/commands" || p.Agents == nil || p.Agents.Dir != "{config}/agents" {
		t.Errorf("commands %+v, agents %+v", p.Commands, p.Agents)
	}
	// Top-level mcpServers of ~/.claude.json, strict JSON.
	if m := p.MCP; m == nil || !slices.Equal(m.Files, []string{"~/.claude.json"}) || m.Format != adapter.JSON || !slices.Equal(m.Path, []string{"mcpServers"}) {
		t.Errorf("mcp: %+v", p.MCP)
	}
}
