// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adapter

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// fake is the smallest thing that satisfies Adapter. That it compiles is the first half of the
// contract: these are the method sets an adapter outside this repository is written against.
type fake struct{ id string }

func (f fake) ID() string       { return f.id }
func (f fake) Profile() Profile { return Profile{ID: f.id, Name: f.id} }
func (f fake) Detect(context.Context, Env) (Detection, error) {
	return Detection{}, nil
}
func (f fake) Capabilities(Detection) Capabilities { return Capabilities{} }
func (f fake) Plan(context.Context, PlanRequest) (Plan, error) {
	return Plan{}, nil
}
func (f fake) AutoApprove(context.Context, Env, Detection) Tri { return TriUnknown }

var (
	_ Adapter      = fake{}
	_ AutoApprover = fake{}
)

// What an adapter gets back when it did not fill something in must be the careful answer: a
// tool nobody could read is unknown, not absent, and it takes nothing.
func TestZeroValuesAreTheCarefulAnswer(t *testing.T) {
	var d Detection
	if d.Status != Unknown {
		t.Errorf("the zero Status is %v, want Unknown", d.Status)
	}
	var tri Tri
	if tri != TriUnknown {
		t.Errorf("the zero Tri is %v, want TriUnknown", tri)
	}
	v := reflect.ValueOf(Capabilities{})
	for i := range v.NumField() {
		if f := v.Field(i); f.Kind() == reflect.Bool && f.Bool() {
			t.Errorf("the zero Capabilities has %s set", v.Type().Field(i).Name)
		}
	}
}

// These names appear in machine-readable output, in manifests and in profiles. Changing one
// breaks whoever reads those, so each is pinned here.
func TestStableNames(t *testing.T) {
	got := map[string]string{
		"Unknown":            Unknown.String(),
		"NotInstalled":       NotInstalled.String(),
		"Installed":          Installed.String(),
		"TriUnknown":         TriUnknown.String(),
		"TriNo":              TriNo.String(),
		"TriYes":             TriYes.String(),
		"Stdio":              string(Stdio),
		"HTTP":               string(HTTP),
		"JSON":               string(JSON),
		"JSONC":              string(JSONC),
		"TOML":               string(TOML),
		"MCPNativeEnv":       string(MCPNativeEnv),
		"MCPLauncher":        string(MCPLauncher),
		"MCPSingleEntry":     string(MCPSingleEntry),
		"ConfigUnreadable":   string(ConfigUnreadable),
		"ConfigUnwritable":   string(ConfigUnwritable),
		"Unsupported":        string(Unsupported),
		"NeedsSecretSupport": string(NeedsSecretSupport),
		"NameTaken":          string(NameTaken),
		"PathTooLong":        string(PathTooLong),
		"LayoutDir":          LayoutDir,
	}
	want := map[string]string{
		"Unknown":            "unknown",
		"NotInstalled":       "not_installed",
		"Installed":          "installed",
		"TriUnknown":         "unknown",
		"TriNo":              "no",
		"TriYes":             "yes",
		"Stdio":              "stdio",
		"HTTP":               "http",
		"JSON":               "json",
		"JSONC":              "jsonc",
		"TOML":               "toml",
		"MCPNativeEnv":       "native-env",
		"MCPLauncher":        "launcher",
		"MCPSingleEntry":     "single-entry",
		"ConfigUnreadable":   "config_unreadable",
		"ConfigUnwritable":   "config_unwritable",
		"Unsupported":        "unsupported",
		"NeedsSecretSupport": "needs_secret_support",
		"NameTaken":          "name_taken",
		"PathTooLong":        "path_too_long",
		"LayoutDir":          "dir",
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %q, want %q", name, got[name], w)
		}
	}
	if s := Status(42).String(); s != "unknown" {
		t.Errorf("an out-of-range Status prints as %q; anything that is not a known answer is unknown", s)
	}
}

func TestFormatValid(t *testing.T) {
	for _, f := range []Format{JSON, JSONC, TOML} {
		if !f.Valid() {
			t.Errorf("Format(%q).Valid() = false", f)
		}
	}
	for _, f := range []Format{"", "yaml", "JSON", "json5"} {
		if f.Valid() {
			t.Errorf("Format(%q).Valid() = true", f)
		}
	}
}

func TestArtifactsReportTheirResourceAndName(t *testing.T) {
	ref := resource.Ref{Kind: resource.KindSkill, Code: "unit-testing", Version: 5}
	artifacts := map[string]Artifact{
		"acme-skill": SkillDir{Resource: ref, DirName: "acme-skill", Files: []File{{Path: "SKILL.md", Data: []byte("x"), Exec: false}}, Degraded: "workflow.as_skill"},
		"acme-mcp": MCPServer{
			Resource: ref, EntryName: "acme-mcp", Transport: HTTP,
			Command: "", Args: nil, Env: map[string]Value{"REGION": {Literal: "eu"}},
			URL: "https://jira.example.com/mcp", Headers: map[string]Value{"Authorization": {Literal: "Bearer ", Secret: "JIRA_API_TOKEN"}},
		},
		"acme-command": Command{Resource: ref, CmdName: "acme-command", Description: "d", Body: "b"},
		"acme-agent":   SubAgent{Resource: ref, AgentName: "acme-agent", Description: "d", Body: "b"},
	}
	for name, a := range artifacts {
		if a.Name() != name {
			t.Errorf("%T.Name() = %q, want %q", a, a.Name(), name)
		}
		if a.Ref() != ref {
			t.Errorf("%T.Ref() = %v, want %v", a, a.Ref(), ref)
		}
	}
}

func TestOpsReportTheirResource(t *testing.T) {
	ref := resource.Ref{Kind: resource.KindMCP, Code: "jira", Version: 4}
	ops := []Op{
		WriteTree{Resource: ref, Dir: "~/.claude/skills/acme-jira", Files: []File{{Path: "SKILL.md"}}},
		WriteFile{Resource: ref, Path: "~/.claude/commands/acme-jira.md", Data: []byte("x")},
		SetKey{Resource: ref, File: "~/.claude.json", Format: JSON, Path: []string{"mcpServers", "acme-jira"}, Value: map[string]any{"type": "http"}},
		Exec{Resource: ref, Argv: []string{"tool", "plugin", "install", "x"}, Undo: []string{"tool", "plugin", "uninstall", "x"}, Effects: []string{"plugin.installs_marketplace"}},
	}
	for _, op := range ops {
		if op.Ref() != ref {
			t.Errorf("%T.Ref() = %v, want %v", op, op.Ref(), ref)
		}
	}
	// A plan is plain data: what the preview ran and what the real write runs can be compared.
	plan := Plan{Ops: ops, Skips: []Skip{{Resource: ref, Reason: ConfigUnreadable, Detail: "line 3"}}}
	if !reflect.DeepEqual(plan, Plan{Ops: ops, Skips: []Skip{{Resource: ref, Reason: ConfigUnreadable, Detail: "line 3"}}}) {
		t.Error("two plans with the same content are not equal")
	}
}

// The profile from the adapter documentation, as the YAML reader hands it over. Every field
// name a profile.yaml may use has to land in the struct.
func TestProfileJSONNamesMatchTheProfileFormat(t *testing.T) {
	const doc = `{
		"id": "opencode",
		"name": "OpenCode",
		"detect": {
			"bins": ["opencode"],
			"dirs": ["{config}"],
			"version": {"run": ["opencode", "--version"], "match": "v?(\\d+\\.\\d+\\.\\d+)"}
		},
		"roots": {"config": {"env": "XDG_CONFIG_HOME", "join": "opencode", "default": "~/.config/opencode"}},
		"verified": {"version": "2.2.7", "os": ["darwin"], "at": "2026-11-24", "how": "ran opencode mcp add --global in a temporary home directory and read the file"},
		"skills": {"dir": "{config}/skills", "layout": "dir"},
		"commands": {"dir": "{config}/commands", "ext": ".md"},
		"agents": {"dir": "{config}/agents", "ext": ".md"},
		"mcp": {"files": ["{config}/opencode.jsonc", "{config}/opencode.json"], "format": "jsonc", "path": ["mcp", "servers"]},
		"alsoReads": [{"kind": "skill", "dir": "~/.claude/skills"}, {"kind": "skill", "dir": "~/.agents/skills"}],
		"credentials": ["auth.json"]
	}`
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.DisallowUnknownFields()
	var p Profile
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("the documented profile does not decode into Profile: %v", err)
	}
	want := Profile{
		ID:   "opencode",
		Name: "OpenCode",
		Detect: Detect{
			Bins:    []string{"opencode"},
			Dirs:    []string{"{config}"},
			Version: &VersionProbe{Run: []string{"opencode", "--version"}, Match: `v?(\d+\.\d+\.\d+)`},
		},
		Roots:       map[string]Root{"config": {Env: "XDG_CONFIG_HOME", Join: "opencode", Default: "~/.config/opencode"}},
		Verified:    &Verification{Version: "2.2.7", OS: []string{"darwin"}, At: "2026-11-24", How: "ran opencode mcp add --global in a temporary home directory and read the file"},
		Skills:      &SkillsTarget{Dir: "{config}/skills", Layout: LayoutDir},
		Commands:    &FilesTarget{Dir: "{config}/commands", Ext: ".md"},
		Agents:      &FilesTarget{Dir: "{config}/agents", Ext: ".md"},
		MCP:         &MCPTarget{Files: []string{"{config}/opencode.jsonc", "{config}/opencode.json"}, Format: JSONC, Path: []string{"mcp", "servers"}},
		AlsoReads:   []SharedDir{{Kind: "skill", Dir: "~/.claude/skills"}, {Kind: "skill", Dir: "~/.agents/skills"}},
		Credentials: []string{"auth.json"},
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("decoded profile:\n got %+v\nwant %+v", p, want)
	}
}

// A profile nobody verified must be recognisable as such after a round trip: the absence of the
// record is what keeps the adapter from writing.
func TestUnverifiedProfileStaysUnverified(t *testing.T) {
	out, err := json.Marshal(Profile{ID: "zcode", Name: "Zcode"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "verified") {
		t.Errorf("an unverified profile encodes with a verification record: %s", out)
	}
	var back Profile
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back.Verified != nil {
		t.Errorf("an unverified profile came back verified: %+v", back.Verified)
	}
}
