// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package opencode

import (
	"slices"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/adapter/adaptertest"
	"github.com/code2rich/rigdepot/ext/adapter/profileyaml"
)

func TestMain(m *testing.M) { adaptertest.Main(m) }

// The places checked on OpenCode 2.2.7 (docs/architecture/04a-platforms.md, and D6 in
// 13-open-questions.md).
func TestProfile(t *testing.T) {
	p, err := profileyaml.Parse(profileYAML)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "opencode" || p.Verified == nil {
		t.Fatalf("id %q, verified %v", p.ID, p.Verified)
	}
	if r := p.Roots["config"]; r.Env != "XDG_CONFIG_HOME" || r.Join != "opencode" || r.Default != "~/.config/opencode" {
		t.Errorf("config root: %+v", r)
	}
	// Plural directory names.
	if p.Skills == nil || p.Skills.Dir != "{config}/skills" {
		t.Errorf("skills: %+v", p.Skills)
	}
	if p.Commands == nil || p.Commands.Dir != "{config}/commands" || p.Agents == nil || p.Agents.Dir != "{config}/agents" {
		t.Errorf("commands %+v, agents %+v", p.Commands, p.Agents)
	}
	// Version 2 lists the servers under mcp.servers.
	if m := p.MCP; m == nil || !slices.Equal(m.Files, []string{"{config}/opencode.jsonc", "{config}/opencode.json"}) ||
		m.Format != adapter.JSONC || !slices.Equal(m.Path, []string{"mcp", "servers"}) {
		t.Errorf("mcp: %+v", p.MCP)
	}
	want := []adapter.SharedDir{{Kind: "skill", Dir: "~/.claude/skills"}, {Kind: "skill", Dir: "~/.agents/skills"}}
	if !slices.Equal(p.AlsoReads, want) {
		t.Errorf("alsoReads: %+v", p.AlsoReads)
	}
}
