// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
)

// The agent and the workflow of the example repository, synced into the three real tools: where
// each lands, OpenCode's sub-agent mode, Codex's fallback to skills, and the agent's memory, which
// survives an update and is kept, not lost, when the agent is uninstalled.
func TestAgentsAndWorkflowsInTheRealTools(t *testing.T) {
	w := newWorld(t)
	w.realTools = true
	for _, dir := range []string{"~/.claude", "~/.codex", "~/.config/opencode"} {
		w.mkdir(dir) // the tools have been used here
	}
	const agent, workflow = "backend-developer", "change-to-merge"
	w.must(cliout.ExitOK, "enable", agent, "--repo", w.repo)
	w.must(cliout.ExitOK, "enable", workflow, "--repo", w.repo)
	w.sync(cliout.ExitOK, "--yes-global", "--tool", "claude-code", "--tool", "codex", "--tool", "opencode")

	name := brand.Prefix + agent
	claudeHome := "~/.claude/skills/" + name
	for _, p := range []string{
		"~/.claude/agents/" + name + ".md",
		claudeHome + "/SKILL.md",
		claudeHome + "/skills/api-development/SKILL.md",
		claudeHome + "/memory/README.md",
		"~/.claude/commands/" + brand.Prefix + workflow + ".md",
		"~/.config/opencode/agents/" + name + ".md",
		"~/.config/opencode/commands/" + brand.Prefix + workflow + ".md",
		"~/.agents/skills/" + name + "/SKILL.md",
		"~/.agents/skills/" + name + "/skills/api-development/SKILL.md",
		"~/.agents/skills/" + brand.Prefix + workflow + "/SKILL.md",
	} {
		if !w.exists(p) {
			t.Errorf("%s was not written", p)
		}
	}
	if text := w.read("~/.config/opencode/agents/" + name + ".md"); !strings.Contains(text, "\nmode: subagent\n") || !strings.Contains(text, "开发自测") {
		t.Errorf("OpenCode's sub-agent:\n%s", text)
	}
	if text := w.read("~/.agents/skills/" + name + "/SKILL.md"); !strings.Contains(text, "no sub-agents") || !strings.Contains(text, "memory/") {
		t.Errorf("Codex gets the whole agent as a skill:\n%s", text)
	}
	for _, p := range []string{"~/.claude/commands/", "~/.config/opencode/commands/"} {
		if text := w.read(p + brand.Prefix + workflow + ".md"); !strings.Contains(text, "flow next "+workflow) {
			t.Errorf("%s: the command does not ask for the steps:\n%s", p, text)
		}
	}

	// The agent keeps notes; that is no change of ours.
	w.write(claudeHome+"/memory/2026-11-25.md", "the shop API wants idempotency keys\n")
	w.write(claudeHome+"/memory/README.md", "my own index of what I learnt\n")
	w.must(cliout.ExitOK, "status", "--repo", w.repo, "--exit-code")

	// A new version of the agent: its instructions and the seed of its memory change.
	w.repoWrite("agents/"+agent+"/AGENT.md", "# 后端工程师\n\n第二版：先写接口说明，再动手。\n")
	w.repoWrite("agents/"+agent+"/memory/README.md", "a new seed nobody should see over their own notes\n")
	w.commit()
	w.sync(cliout.ExitOK, "--yes-global", "--tool", "claude-code", "--tool", "codex", "--tool", "opencode")
	if text := w.read("~/.claude/agents/" + name + ".md"); !strings.Contains(text, "第二版") {
		t.Errorf("the sub-agent was not updated:\n%s", text)
	}
	if got := w.read(claudeHome + "/memory/2026-11-25.md"); got != "the shop API wants idempotency keys\n" {
		t.Errorf("a note in memory/ after the update: %q", got)
	}
	if got := w.read(claudeHome + "/memory/README.md"); got != "my own index of what I learnt\n" {
		t.Errorf("the update overwrote memory/README.md: %q", got)
	}

	// Uninstalling keeps the notes in kept/.
	w.must(cliout.ExitOK, "uninstall")
	if w.exists(claudeHome) {
		t.Error("the agent's directory is still there")
	}
	var found bool
	_ = filepath.WalkDir(w.path("~/"+brand.Home), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, filepath.Join("memory", "2026-11-25.md")) {
			found = true
		}
		return nil
	})
	if !found {
		t.Error("the agent's notes were not kept when it was uninstalled")
	}
}
