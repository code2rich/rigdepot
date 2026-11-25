// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/resource"
)

// memEnv is an Env over files held in memory. Paths are in the package's form.
type memEnv struct {
	goos    string
	home    string
	vars    map[string]string
	files   map[string]string
	dirs    map[string]bool
	broken  map[string]bool // reading these fails with something other than "missing"
	bins    map[string]bool
	version string
}

type memInfo struct{ dir bool }

func (i memInfo) Name() string       { return "x" }
func (i memInfo) Size() int64        { return 0 }
func (i memInfo) Mode() fs.FileMode  { return map[bool]fs.FileMode{true: fs.ModeDir}[i.dir] }
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return i.dir }
func (i memInfo) Sys() any           { return nil }

func (m memEnv) env() Env {
	goos := m.goos
	if goos == "" {
		goos = "linux"
	}
	home := m.home
	if home == "" {
		home = "/home/someone"
	}
	return Env{
		GOOS:   goos,
		Home:   home,
		Getenv: func(k string) string { return m.vars[k] },
		ReadFile: func(p string) ([]byte, error) {
			if m.broken[p] {
				return nil, fs.ErrPermission
			}
			if s, ok := m.files[p]; ok {
				return []byte(s), nil
			}
			return nil, fs.ErrNotExist
		},
		Stat: func(p string) (fs.FileInfo, error) {
			if m.broken[p] {
				return nil, fs.ErrPermission
			}
			if m.dirs[p] {
				return memInfo{dir: true}, nil
			}
			if _, ok := m.files[p]; ok {
				return memInfo{}, nil
			}
			return nil, fs.ErrNotExist
		},
		LookPath: func(name string) (string, error) {
			if m.bins[name] {
				return "/usr/bin/" + name, nil
			}
			return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
		},
		Version: func(context.Context, string, ...string) (string, error) {
			if m.version == "" {
				return "", errors.New("exit status 1")
			}
			return m.version, nil
		},
	}
}

func sampleProfile() Profile {
	return Profile{
		ID:   "examplecode",
		Name: "Example Code",
		Detect: Detect{
			Bins:    []string{"examplecode"},
			Dirs:    []string{"{config}"},
			Version: &VersionProbe{Run: []string{"examplecode", "--version"}, Match: `v?(\d+\.\d+\.\d+)`},
		},
		Roots: map[string]Root{
			"config": {Env: "EXAMPLECODE_HOME", Default: "~/.examplecode"},
			"data":   {Default: "{config}/data"},
		},
		Verified: &Verification{Version: "1.4.0", OS: []string{"linux"}, At: "2026-11-24", How: "by hand"},
		Skills:   &SkillsTarget{Dir: "{config}/skills", Layout: LayoutDir},
		Commands: &FilesTarget{Dir: "{config}/commands", Ext: ".md"},
		Agents:   &FilesTarget{Dir: "{config}/agents", Ext: ".md"},
		MCP:      &MCPTarget{Files: []string{"{config}/settings.jsonc", "{config}/settings.json"}, Format: JSONC, Path: []string{"mcp", "servers"}},
	}
}

func TestFromProfileDetect(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		env  memEnv
		want Detection
	}{
		{"executable found", memEnv{bins: map[string]bool{"examplecode": true}, version: "examplecode v1.4.2\n"},
			Detection{Status: Installed, Version: "1.4.2", Roots: map[string]string{"config": "~/.examplecode", "data": "~/.examplecode/data"}}},
		{"directory found, version unknown", memEnv{dirs: map[string]bool{"~/.examplecode": true}},
			Detection{Status: Installed, Roots: map[string]string{"config": "~/.examplecode", "data": "~/.examplecode/data"}}},
		{"nothing there", memEnv{},
			Detection{Status: NotInstalled, Roots: map[string]string{"config": "~/.examplecode", "data": "~/.examplecode/data"}}},
		{"directory unreadable", memEnv{bins: map[string]bool{"examplecode": true}, broken: map[string]bool{"~/.examplecode": true}},
			Detection{Status: Unknown, Reason: ReasonUnreadable, Roots: map[string]string{"config": "~/.examplecode", "data": "~/.examplecode/data"}}},
		{"root moved inside the home directory", memEnv{bins: map[string]bool{"examplecode": true}, vars: map[string]string{"EXAMPLECODE_HOME": "/home/someone/tools/ec/"}},
			Detection{Status: Installed, Roots: map[string]string{"config": "~/tools/ec", "data": "~/tools/ec/data"}}},
		{"root moved outside the home directory", memEnv{bins: map[string]bool{"examplecode": true}, vars: map[string]string{"EXAMPLECODE_HOME": "/opt/ec"}},
			Detection{Status: Installed, Roots: map[string]string{"config": "/opt/ec", "data": "/opt/ec/data"}}},
		{"relative variable does not count", memEnv{bins: map[string]bool{"examplecode": true}, vars: map[string]string{"EXAMPLECODE_HOME": "ec"}},
			Detection{Status: Installed, Roots: map[string]string{"config": "~/.examplecode", "data": "~/.examplecode/data"}}},
		{"windows path, other case", memEnv{goos: "windows", home: `C:\Users\Someone`, bins: map[string]bool{"examplecode": true}, vars: map[string]string{"EXAMPLECODE_HOME": `c:\users\someone\ec`}},
			Detection{Status: Installed, Roots: map[string]string{"config": "~/ec", "data": "~/ec/data"}}},
		{"unix path on windows does not count", memEnv{goos: "windows", home: `C:\Users\Someone`, bins: map[string]bool{"examplecode": true}, vars: map[string]string{"EXAMPLECODE_HOME": "/opt/ec"}},
			Detection{Status: Installed, Roots: map[string]string{"config": "~/.examplecode", "data": "~/.examplecode/data"}}},
	}
	a := FromProfile(sampleProfile())
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := a.Detect(ctx, c.env.env())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %+v\nwant %+v", got, c.want)
			}
		})
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := a.Detect(cancelled, memEnv{}.env()); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled detection returned %v", err)
	}
	p := sampleProfile()
	p.Roots["config"] = Root{Default: "{data}/x"}
	if _, err := FromProfile(p).Detect(ctx, memEnv{}.env()); err == nil {
		t.Error("roots defined in terms of each other were accepted")
	}
}

func TestFromProfileCapabilities(t *testing.T) {
	got := FromProfile(sampleProfile()).Capabilities(Detection{Status: Installed})
	want := Capabilities{Skills: true, Commands: true, Agents: true, MCP: MCPCaps{Stdio: true, HTTP: true}}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	p := sampleProfile()
	p.Verified = nil
	if got := FromProfile(p).Capabilities(Detection{Status: Installed}); got != (Capabilities{}) {
		t.Errorf("an unverified profile reports %+v", got)
	}
}

var (
	skillRef  = resource.Ref{Kind: resource.KindSkill, Code: "unit-testing", Version: 3}
	stdioRef  = resource.Ref{Kind: resource.KindMCP, Code: "jira", Version: 1}
	secretRef = resource.Ref{Kind: resource.KindMCP, Code: "wiki", Version: 1}
)

// The prefix is spelled with a placeholder here: the adapter does not know it and passes names
// through as they come.
func sampleArtifacts() []Artifact {
	return []Artifact{
		SkillDir{Resource: skillRef, DirName: "p-unit-testing", Files: []File{{Path: "SKILL.md", Data: []byte("# x\n")}}},
		Command{Resource: resource.Ref{Kind: "workflow", Code: "release"}, CmdName: "p-release", Description: `Ship "it"`, Body: "Do it."},
		SubAgent{Resource: resource.Ref{Kind: "agent", Code: "reviewer"}, AgentName: "p-reviewer", Description: "Reviews.", Body: "Review.\n"},
		MCPServer{Resource: stdioRef, EntryName: "p-jira", Transport: Stdio, Command: "jira-mcp", Args: []string{"--stdio"},
			Env: map[string]Value{"MODE": {Literal: "ro"}}},
		MCPServer{Resource: secretRef, EntryName: "p-wiki", Transport: HTTP, URL: "https://wiki.example.com/mcp",
			Headers: map[string]Value{"Authorization": {Literal: "Bearer ", Secret: "WIKI_TOKEN"}}},
	}
}

func plan(t *testing.T, a Adapter, m memEnv) Plan {
	t.Helper()
	env := m.env()
	d, err := a.Detect(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Plan(context.Background(), PlanRequest{Env: env, Detection: d, Artifacts: sampleArtifacts()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFromProfilePlan(t *testing.T) {
	a := FromProfile(sampleProfile())
	got := plan(t, a, memEnv{bins: map[string]bool{"examplecode": true}})
	want := Plan{
		Ops: []Op{
			WriteTree{Resource: skillRef, Dir: "~/.examplecode/skills/p-unit-testing", Files: []File{{Path: "SKILL.md", Data: []byte("# x\n")}}},
			WriteFile{Resource: resource.Ref{Kind: "workflow", Code: "release"}, Path: "~/.examplecode/commands/p-release.md",
				Data: []byte("---\ndescription: \"Ship \\\"it\\\"\"\n---\n\nDo it.\n")},
			WriteFile{Resource: resource.Ref{Kind: "agent", Code: "reviewer"}, Path: "~/.examplecode/agents/p-reviewer.md",
				Data: []byte("---\nname: \"p-reviewer\"\ndescription: \"Reviews.\"\n---\n\nReview.\n")},
			SetKey{Resource: stdioRef, File: "~/.examplecode/settings.json", Format: JSONC, Path: []string{"mcp", "servers", "p-jira"},
				Value: map[string]any{"type": "stdio", "command": "jira-mcp", "args": []any{"--stdio"}, "env": map[string]any{"MODE": "ro"}}},
		},
		Skips: []Skip{{Resource: secretRef, Reason: NeedsSecretSupport,
			Detail: "refers to the personal secret WIKI_TOKEN and the tool has no way to take it from the environment"}},
	}
	if !reflect.DeepEqual(got, want) {
		gj, _ := json.MarshalIndent(got, "", "  ")
		wj, _ := json.MarshalIndent(want, "", "  ")
		t.Errorf("got\n%s\nwant\n%s", gj, wj)
	}
}

func TestFromProfilePlanReadsTheConfiguration(t *testing.T) {
	a := FromProfile(sampleProfile())
	installed := map[string]bool{"examplecode": true}
	mcpOps := func(p Plan) (keys []SetKey) {
		for _, op := range p.Ops {
			if k, ok := op.(SetKey); ok {
				keys = append(keys, k)
			}
		}
		return keys
	}
	reasons := func(p Plan) map[SkipReason]int {
		out := map[SkipReason]int{}
		for _, s := range p.Skips {
			out[s.Reason]++
		}
		return out
	}

	jsonc := "\ufeff{\n  // the person's own comment\n  \"mcp\": {\n    \"servers\": { \"theirs\": {\"type\": \"local\"}, },\n  },\n}\n"
	p := plan(t, a, memEnv{bins: installed, files: map[string]string{"~/.examplecode/settings.jsonc": jsonc}})
	if k := mcpOps(p); len(k) != 1 || k[0].File != "~/.examplecode/settings.jsonc" {
		t.Errorf("JSONC with comments, trailing commas and a byte order mark: got %+v", p)
	}

	for name, m := range map[string]memEnv{
		"does not parse": {files: map[string]string{"~/.examplecode/settings.json": `{"mcp": {`}},
		"empty":          {files: map[string]string{"~/.examplecode/settings.json": ""}},
		"unreadable":     {broken: map[string]bool{"~/.examplecode/settings.jsonc": true}},
	} {
		m.bins = installed
		p := plan(t, a, m)
		if k := mcpOps(p); len(k) != 0 || reasons(p)[ConfigUnreadable] != 2 {
			t.Errorf("%s: want no MCP operation and both servers ConfigUnreadable, got %+v", name, p)
		}
		if len(p.Ops) != 3 {
			t.Errorf("%s: the other artifacts are still planned (the core decides to drop them), got %d ops", name, len(p.Ops))
		}
		for _, s := range p.Skips {
			if strings.Contains(s.Detail, "{") {
				t.Errorf("%s: the detail shows file content: %q", name, s.Detail)
			}
		}
	}

	for name, files := range map[string]map[string]string{
		"both files exist":      {"~/.examplecode/settings.jsonc": "{}", "~/.examplecode/settings.json": "{}"},
		"servers is not a map":  {"~/.examplecode/settings.json": `{"mcp": {"servers": []}}`},
		"top level is an array": {"~/.examplecode/settings.json": `[]`},
	} {
		p := plan(t, a, memEnv{bins: installed, files: files})
		if k := mcpOps(p); len(k) != 0 || reasons(p)[ConfigUnwritable] != 2 {
			t.Errorf("%s: want both servers ConfigUnwritable, got %+v", name, p)
		}
	}

	strict := sampleProfile()
	strict.MCP.Format = JSON
	p = plan(t, FromProfile(strict), memEnv{bins: installed, files: map[string]string{"~/.examplecode/settings.json": "{ // no\n}"}})
	if reasons(p)[ConfigUnreadable] != 2 {
		t.Errorf("a comment in strict JSON was accepted: %+v", p)
	}

	toml := sampleProfile()
	toml.MCP = &MCPTarget{Files: []string{"{config}/config.toml"}, Format: TOML, Path: []string{"mcp_servers"}}
	if p := plan(t, FromProfile(toml), memEnv{bins: installed}); len(mcpOps(p)) != 1 {
		t.Errorf("a TOML file that does not exist yet can be created: %+v", p)
	}
	p = plan(t, FromProfile(toml), memEnv{bins: installed, files: map[string]string{"~/.examplecode/config.toml": "model = \"x\"\n"}})
	if len(mcpOps(p)) != 0 || reasons(p)[ConfigUnwritable] != 2 {
		t.Errorf("an existing TOML file the profile adapter cannot read: %+v", p)
	}
}

func TestFromProfilePlanRefuses(t *testing.T) {
	a := FromProfile(sampleProfile())
	env := memEnv{bins: map[string]bool{"examplecode": true}}.env()
	d, _ := a.Detect(context.Background(), env)
	for _, name := range []string{"", "..", "a/b", `a\b`} {
		_, err := a.Plan(context.Background(), PlanRequest{Env: env, Detection: d,
			Artifacts: []Artifact{SkillDir{Resource: skillRef, DirName: name}}})
		if err == nil {
			t.Errorf("the name %q was accepted", name)
		}
	}
	if _, err := a.Plan(context.Background(), PlanRequest{Env: env, Detection: Detection{Status: Unknown}}); err == nil {
		t.Error("planned for a tool in an unknown state")
	}
	p := sampleProfile()
	p.Verified = nil
	got, err := FromProfile(p).Plan(context.Background(), PlanRequest{Env: env, Detection: d, Artifacts: sampleArtifacts()})
	if err != nil || len(got.Ops) != 0 || len(got.Skips) != len(sampleArtifacts()) {
		t.Errorf("an unverified profile planned %+v, %v", got, err)
	}
}

func TestStandardJSON(t *testing.T) {
	in := "\ufeff{\"a\": \"x // not a comment\", // one\n /* two\n */ \"b\": [1, 2,],\n}"
	out := StandardJSON([]byte(in), true)
	if len(out) != len(in) {
		t.Fatalf("length changed from %d to %d", len(in), len(out))
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	if v["a"] != "x // not a comment" {
		t.Errorf("a string was changed: %q", v["a"])
	}
	if strings.Count(string(out), "\n") != strings.Count(in, "\n") {
		t.Error("line breaks were lost")
	}
	if err := json.Unmarshal(StandardJSON([]byte(`{"a": 1, // x`+"\n}"), false), &v); err == nil {
		t.Error("strict JSON accepted a comment")
	}
}
