// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/executor"
	"github.com/code2rich/rigdepot/internal/manifest"
	"github.com/code2rich/rigdepot/internal/repoformat"
	"github.com/code2rich/rigdepot/internal/sync"
)

// TestReportCarriesNoPaths builds the body of POST /sync/report from a sync whose plan, skips and
// failures are full of paths, file names and the person's account name, and checks the body has
// none of them: only codes, versions, tool ids and stable reasons.
func TestReportCarriesNoPaths(t *testing.T) {
	ref := func(code string, v int) resource.Ref { return resource.Ref{Kind: "skill", Code: code, Version: v} }
	st := &desired.State{
		Resources: []desired.Resource{{Ref: ref("unit-testing", 3)}, {Ref: ref("jira", 2)}, {Ref: ref("review", 1)}, {Ref: ref("lint", 4)}},
		Failed: []desired.Failed{{Ref: ref("broken", 5), Issues: []repoformat.Issue{{File: "/Users/jdoe/" + brand.Home + "/cache/bundles/x",
			Code: "bundle_unavailable", Message: "open /Users/jdoe/" + brand.Home + "/cache/bundles/x: permission denied"}}}},
	}
	target := func(p string) executor.Target { return executor.Target{Op: manifest.OpTree, Path: p} }
	p := &sync.Plan{
		Tools: []sync.Tool{
			{ID: "claude-code", Status: sync.ToolPlanned, Detection: adapter.Detection{Status: adapter.Installed, Version: "2.1.330",
				Roots: map[string]string{"config": "/Users/jdoe/.claude"}},
				Skips: []adapter.Skip{{Resource: ref("jira", 0), Reason: adapter.NeedsSecretSupport,
					Detail: "/Users/jdoe/.claude.json: JIRA_TOKEN is read as empty by remote servers"}}},
			{ID: "codex", Status: sync.ToolUnreadable, Reason: "/Users/jdoe/.codex/config.toml: line 3: expected '='",
				Detection: adapter.Detection{Status: adapter.Installed}},
			{ID: "opencode", Status: sync.ToolNotInstalled, Detection: adapter.Detection{Status: adapter.NotInstalled}},
		},
		Items: []sync.Item{
			{Tool: "claude-code", Resource: ref("old-skill", 7), Action: sync.ActionRevert, ID: "r1", Target: target("~/.claude/skills/" + brand.Prefix + "old-skill")},
			{Tool: "claude-code", Resource: ref("unit-testing", 3), Action: sync.ActionCreate, ID: "w1", Target: target("~/.claude/skills/" + brand.Prefix + "unit-testing")},
			{Tool: "claude-code", Resource: ref("review", 1), Action: sync.ActionModified, ID: "w2", Target: target("~/.claude/skills/" + brand.Prefix + "review")},
			{Tool: "claude-code", Resource: ref("lint", 4), Action: sync.ActionUpdate, ID: "w3", Target: target(`C:\Users\jdoe\.claude\skills\` + brand.Prefix + "lint"),
				Degraded: "workflow.as_skill"},
		},
	}
	res := &sync.Result{
		Done:  []sync.Done{{Item: p.Items[0], Kept: "kept/2026-11-25T10-02/" + brand.Prefix + "old-skill"}, {Item: p.Items[1]}},
		Retry: []sync.Failure{{Item: p.Items[3], Err: errors.New("/Users/jdoe/.claude/skills/" + brand.Prefix + "lint changed")}},
	}
	body := buildReport(st, p, res, false, true, time.Date(2026, 11, 25, 10, 31, 0, 0, time.UTC))
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"/", "\\", "~", "jdoe", ".claude", "config.toml", "JIRA_TOKEN", "kept", "permission"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("the report holds %q:\n%s", leak, data)
		}
	}
	var got []string
	for _, in := range body.Installs {
		s := fmt.Sprintf("%s@%d/%s=%s", in.Code, in.Version, in.Tool, in.Status)
		if in.Reason != nil {
			s += ":" + *in.Reason
		}
		if in.Degraded != nil && *in.Degraded {
			s += "+" + *in.Form
		}
		got = append(got, s)
	}
	want := []string{
		"broken@5/claude-code=skipped:unavailable",
		"jira@2/claude-code=skipped:needs_secret_support",
		"jira@2/codex=skipped:config_unreadable",
		"lint@4/claude-code=failed:changed_while_syncing+skill",
		"lint@4/codex=skipped:config_unreadable",
		"old-skill@7/claude-code=removed",
		"review@1/claude-code=skipped:modified_locally",
		"review@1/codex=skipped:config_unreadable",
		"unit-testing@3/claude-code=installed",
		"unit-testing@3/codex=skipped:config_unreadable",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("installs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if body.Result != "partial" || len(body.Tools) != 2 || body.Tools[0].Tool != "claude-code" || *body.Tools[0].Version != "2.1.330" {
		t.Errorf("result %s, tools %+v", body.Result, body.Tools)
	}
}

// TestReportSingleEntry: the MCP servers behind the single entry are reported where the entry
// is, each as itself; the entry, which is no resource of the server, is not reported.
func TestReportSingleEntry(t *testing.T) {
	mcp := func(code string, v int) resource.Ref { return resource.Ref{Kind: "mcp", Code: code, Version: v} }
	st := &desired.State{Resources: []desired.Resource{{Ref: mcp("jira", 2)}, {Ref: mcp("gitlab", 1)}}}
	key := func(p string) executor.Target {
		return executor.Target{Op: manifest.OpKey, Path: p, Key: []string{"mcpServers", brand.MCPEntry}}
	}
	p := &sync.Plan{
		Tools: []sync.Tool{
			{ID: "claude-code", Status: sync.ToolPlanned, Detection: adapter.Detection{Status: adapter.Installed},
				BehindEntry: []resource.Ref{mcp("jira", 0), mcp("gitlab", 0)}},
			{ID: "codex", Status: sync.ToolPlanned, Detection: adapter.Detection{Status: adapter.Installed},
				BehindEntry: []resource.Ref{mcp("jira", 0), mcp("gitlab", 0)}},
		},
		Items: []sync.Item{
			{Tool: "claude-code", Resource: sync.SingleEntry, Action: sync.ActionCreate, ID: "w1", Target: key("~/.claude.json")},
			{Tool: "codex", Resource: sync.SingleEntry, Action: sync.ActionCreate, ID: "w2", Target: key("~/.codex/config.toml")},
		},
	}
	res := &sync.Result{Done: []sync.Done{{Item: p.Items[0]}}, Deferred: []sync.Item{p.Items[1]}}
	body := buildReport(st, p, res, false, true, time.Date(2026, 11, 25, 10, 31, 0, 0, time.UTC))
	var got []string
	for _, in := range body.Installs {
		s := fmt.Sprintf("%s@%d/%s=%s", in.Code, in.Version, in.Tool, in.Status)
		if in.Reason != nil {
			s += ":" + *in.Reason
		}
		got = append(got, s)
	}
	want := []string{
		"gitlab@1/claude-code=installed",
		"gitlab@1/codex=skipped:consent_needed",
		"jira@2/claude-code=installed",
		"jira@2/codex=skipped:consent_needed",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("installs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
