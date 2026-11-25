// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package codex

import (
	"slices"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/adapter/adaptertest"
	"github.com/code2rich/rigdepot/ext/adapter/profileyaml"
)

func TestMain(m *testing.M) { adaptertest.Main(m) }

// The places checked on codex-cli 0.167.1 (docs/architecture/04a-platforms.md, and D6 in
// 13-open-questions.md).
func TestProfile(t *testing.T) {
	p, err := profileyaml.Parse(profileYAML)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "codex" || p.Verified == nil {
		t.Fatalf("id %q, verified %v", p.ID, p.Verified)
	}
	if r := p.Roots["config"]; r.Env != "CODEX_HOME" || r.Default != "~/.codex" {
		t.Errorf("config root: %+v", r)
	}
	// Skills are in ~/.agents/skills, not under CODEX_HOME: ~/.codex/skills is deprecated.
	if p.Skills == nil || p.Skills.Dir != "~/.agents/skills" {
		t.Errorf("skills: %+v", p.Skills)
	}
	// Custom prompts are deprecated; workflows and agents arrive as skills.
	if p.Commands != nil || p.Agents != nil {
		t.Errorf("commands %+v, agents %+v: Codex has no place for them", p.Commands, p.Agents)
	}
	if m := p.MCP; m == nil || !slices.Equal(m.Files, []string{"{config}/config.toml"}) || m.Format != adapter.TOML || !slices.Equal(m.Path, []string{"mcp_servers"}) {
		t.Errorf("mcp: %+v", p.MCP)
	}
}
