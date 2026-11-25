// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package mcp

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v4"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/kind/kindtest"
	"github.com/code2rich/rigdepot/ext/resource"
)

var update = flag.Bool("update", false, "write testdata/golden from what the kind renders")

func TestMain(m *testing.M) { kindtest.Main(m) }

func TestKind(t *testing.T) { kindtest.Run(t, New(), "testdata") }

var opt = kind.RenderOptions{Prefix: "acme-", CLI: "acme"}

func parse(t *testing.T, code string) spec {
	t.Helper()
	s, err := New().Parse(resource.Ref{Kind: resource.KindMCP, Code: code}, os.DirFS("testdata/valid"))
	if err != nil {
		t.Fatal(err)
	}
	return s.(spec)
}

// deliver picks the form the way the core does and returns its artifacts, or nil when no form
// fits the tool.
func deliver(t *testing.T, s kind.Spec, caps adapter.Capabilities) []adapter.Artifact {
	t.Helper()
	for _, f := range New().Forms() {
		if !f.Needs.SatisfiedBy(caps) {
			continue
		}
		arts, err := f.Render(s, caps, opt)
		if errors.Is(err, kind.ErrNotApplicable) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		return arts
	}
	return nil
}

// A secret in env or headers reaches the adapter as a name, with the text in front of it, and
// never as the ${secret:…} text.
func TestSecretReferencesBecomeStructure(t *testing.T) {
	all := adapter.Capabilities{Skills: true, MCP: adapter.MCPCaps{Stdio: true, HTTP: true, EnvRef: true}}

	jira := parse(t, "jira")
	got := deliver(t, jira, all)
	want := []adapter.Artifact{adapter.MCPServer{
		Resource:  resource.Ref{Kind: resource.KindMCP, Code: "jira"},
		EntryName: "acme-jira",
		Transport: adapter.HTTP,
		URL:       "https://jira.example.com/mcp",
		Headers: map[string]adapter.Value{
			"Authorization": {Literal: "Bearer ", Secret: "JIRA_API_TOKEN"},
			"X-Client":      {Literal: "example-ai"},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("jira:\n got %#v\nwant %#v", got, want)
	}
	if !slices.Equal(jira.Secrets(), []string{"JIRA_API_TOKEN"}) {
		t.Errorf("jira refers to %v", jira.Secrets())
	}

	gitlab := parse(t, "gitlab")
	got = deliver(t, gitlab, all)
	want = []adapter.Artifact{adapter.MCPServer{
		Resource:  resource.Ref{Kind: resource.KindMCP, Code: "gitlab"},
		EntryName: "acme-gitlab",
		Transport: adapter.Stdio,
		Command:   "npx",
		Args:      []string{"-y", "@example/gitlab-mcp@1.4.2"},
		Env: map[string]adapter.Value{
			"GITLAB_TOKEN": {Secret: "GITLAB_TOKEN"},
			"GITLAB_URL":   {Literal: "https://gitlab.example.com"},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gitlab:\n got %#v\nwant %#v", got, want)
	}
	if !slices.Equal(gitlab.Secrets(), []string{"GITLAB_TOKEN"}) {
		t.Errorf("gitlab refers to %v", gitlab.Secrets())
	}
}

// Every place a ${secret:…} must not be is refused, naming the place; every malformed reference
// in a place it may be is refused too. (The fixtures under testdata/invalid do the same through
// the core; this names the cases in one table.)
func TestSecretsOnlyWhereAllowed(t *testing.T) {
	server := func(lines string) string { return "meta: {name: x}\nserver:\n" + lines }
	cases := []struct {
		name, doc, field string
	}{
		{"in the URL", server("  type: http\n  url: https://x.example.com/${secret:JIRA_API_TOKEN}\n"), "server.url"},
		{"in the command", server("  type: stdio\n  command: ${secret:GITLAB_TOKEN}\n"), "server.command"},
		{"in an argument", server("  type: stdio\n  command: x\n  args: [--token, \"${secret:GITLAB_TOKEN}\"]\n"), "server.args.1"},
		{"in an environment variable's name", server("  type: stdio\n  command: x\n  env: {\"${secret:GITLAB_TOKEN}\": a}\n"), "server.env.${secret:GITLAB_TOKEN}"},
		{"in a field of its own", server("  type: stdio\n  command: x\n  x-token: ${secret:GITLAB_TOKEN}\n"), "server.x-token"},
		{"in a tool", server("  type: stdio\n  command: x\n") + "tools: [{name: \"${secret:A_B}\"}]\n", "tools.0.name"},
		{"in the middle of a header", server("  type: http\n  url: https://x.example.com\n  headers: {A: \"${secret:JIRA_API_TOKEN} x\"}\n"), "server.headers.A"},
		{"twice in a header", server("  type: http\n  url: https://x.example.com\n  headers: {A: \"${secret:A_B}${secret:A_B}\"}\n"), "server.headers.A"},
		{"with a malformed name", server("  type: stdio\n  command: x\n  env: {A: \"${secret:1A}\"}\n"), "server.env.A"},
		{"with an empty name", server("  type: stdio\n  command: x\n  env: {A: \"${secret:}\"}\n"), "server.env.A"},
		{"not closed", server("  type: stdio\n  command: x\n  env: {A: \"${secret:A_B\"}\n"), "server.env.A"},
		{"not closed before a good one", server("  type: stdio\n  command: x\n  env: {A: \"${secret:A ${secret:A_B}\"}\n"), "server.env.A"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New().Parse(resource.Ref{Kind: resource.KindMCP, Code: "probe"}, fstest.MapFS{"probe.yaml": {Data: []byte(c.doc)}})
			if err == nil || !strings.Contains(err.Error(), c.field+":") {
				t.Errorf("Parse: %v; want a refusal naming %s", err, c.field)
			}
		})
	}
}

// Which form each server takes, by what the tool supports and how the server is meant to be
// delivered.
func TestFormsByToolAndDelivery(t *testing.T) {
	var (
		everything = adapter.Capabilities{Skills: true, Commands: true, Agents: true, MCP: adapter.MCPCaps{Stdio: true, HTTP: true, EnvRef: true}}
		skillsOnly = adapter.Capabilities{Skills: true}
		mcpOnly    = adapter.Capabilities{MCP: adapter.MCPCaps{Stdio: true, HTTP: true}}
		stdioSkill = adapter.Capabilities{Skills: true, MCP: adapter.MCPCaps{Stdio: true}}
		httpSkill  = adapter.Capabilities{Skills: true, MCP: adapter.MCPCaps{HTTP: true}}
	)
	// "server" is the native entry; "skill" the on-demand skill; "fallback" the same skill
	// marked as a fallback; "" nothing.
	cases := []struct {
		code string
		caps adapter.Capabilities
		want string
	}{
		{"jira", everything, "server"},
		{"jira", httpSkill, "server"},
		{"jira", stdioSkill, "fallback"},
		{"jira", skillsOnly, "fallback"},
		{"jira", adapter.Capabilities{MCP: adapter.MCPCaps{Stdio: true}}, ""},
		{"gitlab", everything, "server"},
		{"gitlab", stdioSkill, "server"},
		{"gitlab", httpSkill, ""}, // lists no tools, so no skill could help
		{"wiki-search", everything, "skill"},
		{"wiki-search", skillsOnly, "skill"},
		{"wiki-search", mcpOnly, ""},
		{"build-events", everything, "fallback"},
		{"build-events", mcpOnly, ""},
	}
	for _, c := range cases {
		arts := deliver(t, parse(t, c.code), c.caps)
		got := ""
		switch {
		case len(arts) == 0:
		case len(arts) > 1:
			got = "several"
		default:
			switch a := arts[0].(type) {
			case adapter.MCPServer:
				got = "server"
			case adapter.SkillDir:
				got = "skill"
				if a.Degraded == AsSkill {
					got = "fallback"
				}
			}
		}
		if got != c.want {
			t.Errorf("%s for a tool with %+v: %q, want %q", c.code, c.caps, got, c.want)
		}
	}
}

// The skill of on-demand delivery tells the AI how to call the tools and nothing about how the
// server is reached. The golden files are the review of its text: go test -run Golden -update.
func TestOnDemandSkillGolden(t *testing.T) {
	for _, c := range []struct {
		code string
		caps adapter.Capabilities
	}{
		{"wiki-search", adapter.Capabilities{Skills: true}},
		{"jira", adapter.Capabilities{Skills: true}},
	} {
		t.Run(c.code, func(t *testing.T) {
			arts := deliver(t, parse(t, c.code), c.caps)
			if len(arts) != 1 {
				t.Fatalf("%d artifacts", len(arts))
			}
			sd := arts[0].(adapter.SkillDir)
			if len(sd.Files) != 1 || sd.Files[0].Path != "SKILL.md" {
				t.Fatalf("files %v, want just SKILL.md", sd.Files)
			}
			data := sd.Files[0].Data

			golden := filepath.Join("testdata", "golden", c.code+".SKILL.md")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, want) {
				t.Errorf("SKILL.md differs from %s:\n%s", golden, data)
			}

			var front struct{ Name, Description string }
			body, ok := bytes.CutPrefix(data, []byte("---\n"))
			fm, _, ok2 := bytes.Cut(body, []byte("\n---\n"))
			if !ok || !ok2 {
				t.Fatalf("no front matter:\n%s", data)
			}
			if err := yaml.Unmarshal(fm, &front); err != nil {
				t.Fatal(err)
			}
			if front.Name != "acme-"+c.code || front.Description == "" {
				t.Errorf("front matter %+v, want the skill named after its directory", front)
			}
			if !bytes.Contains(data, []byte("acme mcp call "+c.code+" <tool>")) {
				t.Errorf("the skill does not say how to call the tools:\n%s", data)
			}
			for _, leak := range []string{"${secret:", "_TOKEN", ".example.com"} {
				if bytes.Contains(data, []byte(leak)) {
					t.Errorf("the skill holds %q; how the server is reached and its secrets are not the AI's business", leak)
				}
			}
		})
	}
}

func TestSkillNeedsTheNameOfTheCommandLine(t *testing.T) {
	_, err := asOnDemandSkill(parse(t, "wiki-search"), adapter.Capabilities{Skills: true}, kind.RenderOptions{Prefix: "acme-"})
	if err == nil || errors.Is(err, kind.ErrNotApplicable) {
		t.Errorf("rendering without RenderOptions.CLI: %v; want a failure, not a skill that names no program", err)
	}
}

func TestLongDescriptionsAreCut(t *testing.T) {
	s := parse(t, "wiki-search")
	s.summary = strings.Repeat("长", 2000)
	arts, err := asOnDemandSkill(s, adapter.Capabilities{Skills: true}, opt)
	if err != nil {
		t.Fatal(err)
	}
	data := arts[0].(adapter.SkillDir).Files[0].Data
	var front struct{ Description string }
	fm, _, _ := bytes.Cut(bytes.TrimPrefix(data, []byte("---\n")), []byte("\n---\n"))
	if err := yaml.Unmarshal(fm, &front); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(front.Description)); n != maxDescription || !strings.HasSuffix(front.Description, "…") {
		t.Errorf("description has %d characters, want %d ending in an ellipsis", n, maxDescription)
	}
}
