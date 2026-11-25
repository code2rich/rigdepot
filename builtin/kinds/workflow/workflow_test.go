// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package workflow

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

	"go.yaml.in/yaml/v4"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/kind/kindtest"
	"github.com/code2rich/rigdepot/ext/resource"
)

var update = flag.Bool("update", false, "write testdata/golden from what the kind renders")

func TestMain(m *testing.M) { kindtest.Main(m) }

func TestKind(t *testing.T) { kindtest.Run(t, New(), "testdata") }

var opt = kind.RenderOptions{Prefix: "acme-", CLI: "acme", Data: "~/.acme"}

func parse(t *testing.T, code string) spec {
	t.Helper()
	s, err := New().Parse(resource.Ref{Kind: resource.KindWorkflow, Code: code}, os.DirFS("testdata/valid"))
	if err != nil {
		t.Fatal(err)
	}
	return s.(spec)
}

func deliver(t *testing.T, s kind.Spec, caps adapter.Capabilities) (kind.Form, []adapter.Artifact) {
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
		return f, arts
	}
	return kind.Form{}, nil
}

// The steps reach the command line as they are written, with "continue" where then is left out.
func TestStepsAndReferences(t *testing.T) {
	s := parse(t, "task-to-submit")
	want := []kind.Step{
		{Use: "task-development", With: []string{"eng-handbook"}, Then: kind.ThenContinue, Checks: []kind.Check{
			{Text: "改动没有超出任务范围", By: kind.CheckByAI},
			{Text: "工程能编译通过", By: kind.CheckByCommand, Run: "./gradlew compileJava -q"},
		}},
		{Use: "unit-testing", Then: kind.ThenContinue, Checks: []kind.Check{
			{Text: "单元测试全部通过", By: kind.CheckByCommand, Run: "./gradlew test -q"},
		}},
		{Use: "delivery-check", Then: kind.ThenConfirm, Forbid: []string{"推送代码"}, Checks: []kind.Check{
			{Text: "自查结论已由人确认", By: kind.CheckByHuman},
		}},
	}
	if got := s.Steps(); !reflect.DeepEqual(got, want) {
		t.Errorf("Steps:\n got %#v\nwant %#v", got, want)
	}
	if got := s.Refs(); !slices.Equal(got, []string{"delivery-check", "eng-handbook", "task-development", "unit-testing"}) {
		t.Errorf("Refs = %v", got)
	}
	// What Steps returns is the caller's.
	s.Steps()[0].Checks[0].Text = "changed"
	if s.Steps()[0].Checks[0].Text == "changed" {
		t.Error("Steps hands out the spec's own slices")
	}
}

// A slash command where the tool has them, a skill marked as the fallback where it has skills
// only, nothing where it has neither.
func TestFormsByTool(t *testing.T) {
	s := parse(t, "task-to-submit")
	if f, arts := deliver(t, s, adapter.Capabilities{Skills: true, Commands: true}); len(arts) != 1 || f.Degraded != "" {
		t.Errorf("a tool with commands: %v (%q)", arts, f.Degraded)
	} else if _, ok := arts[0].(adapter.Command); !ok {
		t.Errorf("a tool with commands gets a %T", arts[0])
	}
	if f, arts := deliver(t, s, adapter.Capabilities{Skills: true}); len(arts) != 1 || f.Degraded != AsSkill {
		t.Errorf("a tool with skills only: %v (%q)", arts, f.Degraded)
	} else if sd, ok := arts[0].(adapter.SkillDir); !ok || sd.Degraded != AsSkill {
		t.Errorf("a tool with skills only gets %#v", arts[0])
	}
	if _, arts := deliver(t, s, adapter.Capabilities{MCP: adapter.MCPCaps{Stdio: true}}); arts != nil {
		t.Errorf("a tool with neither gets %v", arts)
	}
}

// The texts are reviewed in the golden files: go test -run Golden -update. Neither holds a step:
// the command line hands those out.
func TestGolden(t *testing.T) {
	s := parse(t, "task-to-submit")
	_, cmd := deliver(t, s, adapter.Capabilities{Commands: true})
	_, skill := deliver(t, s, adapter.Capabilities{Skills: true})
	c := cmd[0].(adapter.Command)
	sd := skill[0].(adapter.SkillDir)
	if c.CmdName != "acme-task-to-submit" || sd.DirName != "acme-task-to-submit" {
		t.Errorf("names %q and %q", c.CmdName, sd.DirName)
	}
	for name, data := range map[string][]byte{
		"task-to-submit.command.md": []byte("description: " + c.Description + "\n---\n" + c.Body),
		"task-to-submit.SKILL.md":   sd.Files[0].Data,
	} {
		golden := filepath.Join("testdata", "golden", name)
		if *update {
			if err := os.WriteFile(golden, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, want) {
			t.Errorf("%s differs:\n%s", golden, data)
		}
		for _, leak := range []string{"gradlew", "单元测试全部通过", "推送代码", "unit-testing"} {
			if bytes.Contains(data, []byte(leak)) {
				t.Errorf("%s holds %q; the steps are handed out by the command line, one at a time", name, leak)
			}
		}
		if !bytes.Contains(data, []byte("acme flow next task-to-submit")) {
			t.Errorf("%s does not say how to get the next step", name)
		}
	}
	if !strings.HasSuffix(c.Body, "$ARGUMENTS\n") {
		t.Error("the command does not pass on what the person typed after it")
	}

	var front struct{ Name, Description string }
	fm, _, _ := bytes.Cut(bytes.TrimPrefix(sd.Files[0].Data, []byte("---\n")), []byte("\n---\n"))
	if err := yaml.Unmarshal(fm, &front); err != nil {
		t.Fatal(err)
	}
	if front.Name != "acme-task-to-submit" || !strings.Contains(front.Description, "任务开发") {
		t.Errorf("front matter %+v", front)
	}
}

func TestRenderingNeedsTheNameOfTheCommandLine(t *testing.T) {
	_, err := asCommand(parse(t, "one-step"), adapter.Capabilities{Commands: true}, kind.RenderOptions{Prefix: "acme-"})
	if err == nil || errors.Is(err, kind.ErrNotApplicable) {
		t.Errorf("rendering without RenderOptions.CLI: %v; want a failure", err)
	}
}
