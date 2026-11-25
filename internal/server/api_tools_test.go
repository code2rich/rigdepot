// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/adapter/profileyaml"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// builtinAdapters reads the built-in adapters' profiles from the source tree: the core may not
// import builtin/ (rule R3), but the program links them in through builtin/all, and the page
// shows nothing but what these files say.
func builtinAdapters(t *testing.T) []adapter.Adapter {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "builtin", "adapters", "*", "profile.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no built-in profiles: %v", err)
	}
	var out []adapter.Adapter
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		p, err := profileyaml.Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out = append(out, adapter.FromProfile(p))
	}
	return out
}

func listAdapters(t *testing.T, f *apiFixture) map[string]httpapi.Adapter {
	t.Helper()
	rec, _ := f.call(t, http.MethodGet, "/api/v1/adapters", testAdmin, "", nil, http.StatusOK)
	var list httpapi.AdapterList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]httpapi.Adapter{}
	for _, a := range list.Items {
		out[a.Id] = a
	}
	return out
}

func target(a httpapi.Adapter, kind string) (location, detail, fallback string) {
	tg, ok := a.Targets[kind]
	if !ok {
		return "<absent>", "", ""
	}
	if tg.Location != nil {
		location = *tg.Location
	}
	if tg.Detail != nil {
		detail = *tg.Detail
	}
	if tg.Fallback != nil {
		fallback = *tg.Fallback
	}
	return location, detail, fallback
}

// TestToolAdaptersPage: what the "tool adapters" page (D-02) shows of each built-in tool comes
// from its profile: where skills, MCP servers, slash commands and sub-agents land, which kinds
// fall back to a skill, and the version it was verified against.
func TestToolAdaptersPage(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{adapters: builtinAdapters(t)})
	got := listAdapters(t, f)
	type row struct{ kind, location, detail, fallback string }
	want := map[string][]row{
		"claude-code": {
			{"skill", "~/.claude/skills/", "", ""},
			{"mcp", "~/.claude.json", "mcpServers", ""},
			{"command", "~/.claude/commands/", "", ""},
			{"agent", "~/.claude/agents/", "", ""},
		},
		"codex": {
			{"skill", "~/.agents/skills/", "", ""},
			{"mcp", "~/.codex/config.toml", "mcp_servers", ""},
			{"command", "", "", "skill"},
			{"agent", "", "", "skill"},
		},
		"opencode": {
			{"skill", "~/.config/opencode/skills/", "", ""},
			{"mcp", "~/.config/opencode/opencode.json", "mcp.servers", ""},
			{"command", "~/.config/opencode/commands/", "", ""},
			{"agent", "~/.config/opencode/agents/", "", ""},
		},
	}
	if len(got) != len(want) {
		t.Errorf("adapters: %v", got)
	}
	for id, rows := range want {
		a, ok := got[id]
		if !ok {
			t.Errorf("%s is not listed", id)
			continue
		}
		if !a.Enabled || a.People != 0 || a.Verified == nil || a.Verified.Version == "" || a.Verified.At.IsZero() || a.Name == "" {
			t.Errorf("%s: %+v", id, a)
		}
		if _, ok := a.Targets["hook"]; ok {
			t.Errorf("%s: a hook target, which no profile describes yet", id)
		}
		for _, r := range rows {
			if l, d, fb := target(a, r.kind); l != r.location || d != r.detail || fb != r.fallback {
				t.Errorf("%s %s: location %q, detail %q, fallback %q; want %q, %q, %q", id, r.kind, l, d, fb, r.location, r.detail, r.fallback)
			}
		}
	}
	if a := got["claude-code"]; a.Name != "Claude Code" || a.Verified.Version != "2.1.330" || a.Verified.At.String() != "2026-11-25" {
		t.Errorf("claude-code: %s %+v", a.Name, a.Verified)
	}
}

// TestToolAdaptersFollowTheProfile: the page has no table of its own: a different profile shows
// differently, roots at their defaults, and a profile without a verification record has none.
func TestToolAdaptersFollowTheProfile(t *testing.T) {
	p := adapter.Profile{
		ID: "fake-tool", Name: "Fake Tool",
		Roots:  map[string]adapter.Root{"config": {Env: "FAKE_TOOL_HOME", Default: "~/.fake-tool"}},
		Skills: &adapter.SkillsTarget{Dir: "{config}/skill-dir", Layout: adapter.LayoutDir},
		Agents: &adapter.FilesTarget{Dir: "~/elsewhere/agents", Ext: ".md"},
	}
	f := newAPIWith(t, fixtureOptions{adapters: []adapter.Adapter{adapter.FromProfile(p)}})
	a := listAdapters(t, f)["fake-tool"]
	if a.Verified != nil || a.Name != "Fake Tool" {
		t.Errorf("unverified profile: %+v", a)
	}
	for kind, want := range map[string][3]string{
		"skill": {"~/.fake-tool/skill-dir/", "", ""}, "agent": {"~/elsewhere/agents/", "", ""},
		"command": {"", "", "skill"}, "mcp": {"", "", "skill"},
	} {
		if l, d, fb := target(a, kind); [3]string{l, d, fb} != want {
			t.Errorf("%s: %q %q %q, want %v", kind, l, d, fb, want)
		}
	}
}

// TestToolSwitches: an administrator turns a tool off and on for the organization; each change
// is audited, a member can neither see nor change the switches, and unknown tools are 404.
func TestToolSwitches(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{adapters: builtinAdapters(t)})
	f.call(t, http.MethodGet, "/api/v1/adapters", "", "", nil, http.StatusUnauthorized)
	f.call(t, http.MethodGet, "/api/v1/adapters", testMember, "", nil, http.StatusForbidden)
	f.call(t, http.MethodPatch, "/api/v1/adapters/codex", testMember, `{"enabled":false}`, nil, http.StatusForbidden)
	f.call(t, http.MethodPatch, "/api/v1/adapters/no-such-tool", testAdmin, `{"enabled":false}`, nil, http.StatusNotFound)

	rec, _ := f.call(t, http.MethodPatch, "/api/v1/adapters/codex", testAdmin, `{"enabled":false}`, nil, http.StatusOK)
	var a httpapi.Adapter
	json.Unmarshal(rec.Body.Bytes(), &a)
	if a.Id != "codex" || a.Enabled {
		t.Errorf("turned off: %s", rec.Body)
	}
	if got := listAdapters(t, f); got["codex"].Enabled || !got["claude-code"].Enabled {
		t.Errorf("after turning codex off: %+v", got)
	}
	if ids, err := f.app.settings.DisabledAdapters(context.Background()); err != nil || len(ids) != 1 || ids[0] != "codex" {
		t.Errorf("disabled in the settings: %v %v", ids, err)
	}
	f.call(t, http.MethodPatch, "/api/v1/adapters/codex", testAdmin, `{"enabled":false}`, nil, http.StatusOK) // no change
	f.call(t, http.MethodPatch, "/api/v1/adapters/codex", testAdmin, `{"enabled":true}`, nil, http.StatusOK)
	if got := listAdapters(t, f); !got["codex"].Enabled {
		t.Error("codex is still off")
	}
	off, on := auditRows(t, f, settings.ActionAdapterDisable), auditRows(t, f, settings.ActionAdapterEnable)
	if len(on) != 1 || len(off) != 2 || on[0].Object.Label != "Codex" || off[0].Result != audit.ResultOK || off[1].Result != audit.ResultDenied {
		t.Errorf("audit: on %+v, off %+v", on, off)
	}
}

func TestCLIVersions(t *testing.T) {
	for server, want := range map[string][2]string{
		"0.5.2": {"0.5.2", "0.4.0"}, "1.0.0": {"1.0.0", "1.0.0"}, "0.6.0-rc.1": {"0.6.0-rc.1", "0.5.0"},
		"0.0.0-dev+0123abcd": {"0.0.0-dev", "0.0.0"}, "(devel)": {"0.0.0", "0.0.0"},
	} {
		if latest, floor := cliVersions(server); latest != want[0] || floor != want[1] {
			t.Errorf("cliVersions(%q) = %q, %q; want %v", server, latest, floor, want)
		}
	}
}

// TestCLIPolicy: the minimum command-line version starts at what the server supports, can be
// raised, cannot go below the floor, and every change is audited.
func TestCLIPolicy(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{version: "0.5.2"})
	get := func() httpapi.CLIPolicy {
		t.Helper()
		rec, _ := f.call(t, http.MethodGet, "/api/v1/cli", testAdmin, "", nil, http.StatusOK)
		var p httpapi.CLIPolicy
		json.Unmarshal(rec.Body.Bytes(), &p)
		return p
	}
	if p := get(); p.MinVersion != "0.4.0" || p.MinVersionFloor != "0.4.0" || p.LatestVersion != "0.5.2" || p.Distribution == nil {
		t.Errorf("fresh: %+v", p)
	}
	f.call(t, http.MethodGet, "/api/v1/cli", testMember, "", nil, http.StatusForbidden)
	f.call(t, http.MethodPatch, "/api/v1/cli", testMember, `{"min_version":"0.5.0"}`, nil, http.StatusForbidden)

	rec, _ := f.call(t, http.MethodPatch, "/api/v1/cli", testAdmin, `{"min_version":"0.5.0"}`, nil, http.StatusOK)
	if p := get(); p.MinVersion != "0.5.0" || !json.Valid(rec.Body.Bytes()) {
		t.Errorf("raised: %+v", p)
	}
	_, p := f.call(t, http.MethodPatch, "/api/v1/cli", testAdmin, `{"min_version":"0.3.9"}`, nil, http.StatusConflict)
	if p.Code != "cli.below_floor" || p.Params == nil || (*p.Params)["floor"] != "0.4.0" {
		t.Errorf("below the floor: %+v", p)
	}
	_, p = f.call(t, http.MethodPatch, "/api/v1/cli", testAdmin, `{"min_version":"latest"}`, nil, http.StatusBadRequest)
	if p.Errors == nil || (*p.Errors)[0].Field != "min_version" {
		t.Errorf("not a version: %+v", p)
	}
	if p := get(); p.MinVersion != "0.5.0" {
		t.Errorf("after refused changes: %+v", p)
	}
	rows := auditRows(t, f, settings.ActionCLIPolicyUpdate)
	var ok []audit.Row
	for _, r := range rows {
		if r.Result == audit.ResultOK {
			ok = append(ok, r)
		}
	}
	if len(ok) != 1 || ok[0].Change == "" {
		t.Errorf("audit: %+v", rows)
	}

	// A minimum set under an older server that this one no longer supports shows the floor.
	g := newAPIWith(t, fixtureOptions{version: "0.7.0"})
	if err := g.app.settings.SetCLIMinVersion(context.Background(), audit.Actor{UserID: testAdmin, Origin: audit.OriginWeb}, "0.5.0", "0.4.0"); err != nil {
		t.Fatal(err)
	}
	rec, _ = g.call(t, http.MethodGet, "/api/v1/cli", testAdmin, "", nil, http.StatusOK)
	var q httpapi.CLIPolicy
	json.Unmarshal(rec.Body.Bytes(), &q)
	if q.MinVersion != "0.6.0" || q.MinVersionFloor != "0.6.0" {
		t.Errorf("older minimum: %+v", q)
	}
}
