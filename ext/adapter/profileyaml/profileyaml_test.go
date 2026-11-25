// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package profileyaml

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// samples is where the profile schema keeps its samples. This package and the schema check the
// same rules, so they are held to the same samples.
var samples = filepath.Join("..", "..", "..", "schema", "adapter", "v1", "testdata", "profile")

func sampleFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(samples, dir, "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no samples in %s: %v", filepath.Join(samples, dir), err)
	}
	return files
}

func TestValidSamplesParse(t *testing.T) {
	for _, file := range sampleFiles(t, "valid") {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		p, err := Parse(data)
		if err != nil {
			t.Errorf("%s passes the schema and is refused here: %v", filepath.Base(file), err)
			continue
		}
		if p.ID == "" || p.Name == "" {
			t.Errorf("%s: parsed into %+v", filepath.Base(file), p)
		}
	}
}

// Every invalid sample is named "<field>--<what is wrong>" and shows exactly one fault. Parse has
// to refuse it at that field and nowhere else.
func TestInvalidSamplesAreRefusedAtTheirField(t *testing.T) {
	for _, file := range sampleFiles(t, "invalid") {
		name := filepath.Base(file)
		field, _, _ := strings.Cut(strings.TrimSuffix(name, ".yaml"), "--")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Parse(data)
		var perr *Error
		if !errors.As(err, &perr) {
			t.Errorf("%s: want a list of problems, got %v", name, err)
			continue
		}
		named := false
		for _, p := range perr.Problems {
			named = named || p.Field == field
			if p.Field != field && !strings.HasPrefix(p.Field, field+".") {
				t.Errorf("%s: the sample has one fault, at %s, and Parse also reports %s", name, field, p)
			}
		}
		if !named {
			t.Errorf("%s: refused, but not at %s: %v", name, field, err)
		}
	}
}

func TestParseKeepsEverything(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(samples, "valid", "opencode.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	want := adapter.Profile{
		ID:   "opencode",
		Name: "OpenCode",
		Detect: adapter.Detect{
			Bins:    []string{"opencode"},
			Dirs:    []string{"{config}"},
			Version: &adapter.VersionProbe{Run: []string{"opencode", "--version"}, Match: `v?(\d+\.\d+\.\d+)`},
		},
		Roots:    map[string]adapter.Root{"config": {Env: "XDG_CONFIG_HOME", Join: "opencode", Default: "~/.config/opencode"}},
		Verified: &adapter.Verification{Version: "2.2.7", OS: []string{"darwin"}, At: "2026-11-24", How: "临时 HOME 里 opencode mcp add --global 后读出文件"},
		Skills:   &adapter.SkillsTarget{Dir: "{config}/skills", Layout: adapter.LayoutDir},
		Commands: &adapter.FilesTarget{Dir: "{config}/commands", Ext: ".md"},
		Agents:   &adapter.FilesTarget{Dir: "{config}/agents", Ext: ".md"},
		MCP:      &adapter.MCPTarget{Files: []string{"{config}/opencode.jsonc", "{config}/opencode.json"}, Format: adapter.JSONC, Path: []string{"mcp", "servers"}},
		AlsoReads: []adapter.SharedDir{
			{Kind: "skill", Dir: "~/.claude/skills"},
			{Kind: "skill", Dir: "~/.agents/skills"},
		},
		Credentials: []string{"auth.json"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

const minimal = "id: examplecode\nname: Example Code\ndetect:\n  bins: [examplecode]\n"

// What the schema cannot say, and what is not a profile at all.
func TestParseRefuses(t *testing.T) {
	cases := map[string]struct{ yaml, field string }{
		"undeclared root":          {minimal + "skills: { dir: \"{config}/skills\", layout: dir }\n", "skills.dir"},
		"match without group":      {minimal + "  version: { run: [examplecode, --version], match: '\\d+' }\n", "detect.version.match"},
		"match does not parse":     {minimal + "  version: { run: [examplecode, --version], match: '(' }\n", "detect.version.match"},
		"date that does not exist": {minimal + "verified: { version: \"1\", os: [linux], at: 2026-02-30, how: x }\n", "verified.at"},
		"os twice":                 {minimal + "verified: { version: \"1\", os: [linux, linux], at: \"2026-02-03\", how: x }\n", "verified.os.1"},
		"version as a number":      {minimal + "verified: { version: 2.10, os: [linux], at: \"2026-02-03\", how: x }\n", "verified.version"},
		"root defined twice":       {minimal + "roots:\n  config: { default: \"~/.a\" }\n  config: { default: \"~/.b\" }\n", ""},
		"two documents":            {minimal + "---\n" + minimal, ""},
		"empty":                    {"", ""},
		"a list":                   {"- id: examplecode\n", ""},
		"an alias":                 {"id: &x examplecode\nname: *x\ndetect:\n  bins: [examplecode]\n", ""},
		"not YAML":                 {"id: [examplecode\n", ""},
	}
	for name, c := range cases {
		_, err := Parse([]byte(c.yaml))
		var perr *Error
		if !errors.As(err, &perr) {
			t.Errorf("%s: want a list of problems, got %v", name, err)
			continue
		}
		found := false
		for _, p := range perr.Problems {
			found = found || p.Field == c.field
		}
		if !found {
			t.Errorf("%s: want a problem at %q, got %v", name, c.field, err)
		}
	}
}

func TestMustParsePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustParse returned for a profile without an id")
		}
	}()
	MustParse([]byte("name: x\n"))
}

func TestDateWithoutQuotesStaysText(t *testing.T) {
	p, err := Parse([]byte(minimal + "verified: { version: \"1\", os: [linux], at: 2026-11-24, how: x }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Verified.At != "2026-11-24" {
		t.Errorf("at is %q", p.Verified.At)
	}
}
