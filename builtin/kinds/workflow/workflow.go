// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package workflow

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
)

//go:embed schema.json
var schemaJSON []byte

// AsSkill is the message key of the explanation for a workflow that reaches a tool without slash
// commands as a skill.
const AsSkill = "workflow.as_skill"

// New returns the workflow kind.
func New() kind.Kind { return workflowKind{} }

type workflowKind struct{}

func (workflowKind) ID() string { return resource.KindWorkflow }
func (workflowKind) Layout() kind.Layout {
	return kind.Layout{Dir: "workflows", File: true, Ext: ".yaml"}
}
func (workflowKind) Schema() []byte { return slices.Clone(schemaJSON) }

// Forms: a slash command where the tool has those, a skill elsewhere (decision D6: Codex).
func (workflowKind) Forms() []kind.Form {
	return []kind.Form{
		{Needs: kind.Needs{Commands: true}, Render: asCommand},
		{Needs: kind.Needs{Skills: true}, Degraded: AsSkill, Render: asSkill},
	}
}

// spec is a parsed workflow.
type spec struct {
	ref                 resource.Ref
	name, summary, when string
	steps               []kind.Step
}

var (
	_ kind.Flow     = spec{}
	_ kind.Referrer = spec{}
)

// Steps implements kind.Flow.
func (s spec) Steps() []kind.Step {
	out := make([]kind.Step, len(s.steps))
	for i, st := range s.steps {
		st.With = slices.Clone(st.With)
		st.Checks = slices.Clone(st.Checks)
		st.Forbid = slices.Clone(st.Forbid)
		out[i] = st
	}
	return out
}

// Refs implements kind.Referrer: what the steps use.
func (s spec) Refs() []string {
	var out []string
	for _, st := range s.steps {
		out = append(out, st.Use)
		out = append(out, st.With...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// file is workflows/<code>.yaml as written.
type file struct {
	Meta struct {
		Name    string `yaml:"name"`
		Summary string `yaml:"summary"`
		When    string `yaml:"when"`
	} `yaml:"meta"`
	Steps []struct {
		Use    string   `yaml:"use"`
		With   []string `yaml:"with"`
		Checks []struct {
			Text string `yaml:"text"`
			By   string `yaml:"by"`
			Run  string `yaml:"run"`
		} `yaml:"checks"`
		Forbid []string `yaml:"forbid"`
		Then   string   `yaml:"then"`
	} `yaml:"steps"`
}

// Parse reads workflows/<code>.yaml and checks what the schema does not: every problem is reported
// with its field, such as steps.1.checks.0.run.
func (workflowKind) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	name := ref.Code + ".yaml"
	data, err := fs.ReadFile(files, name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	s := spec{ref: ref, name: f.Meta.Name, summary: f.Meta.Summary, when: f.Meta.When}
	var problems []string
	problem := func(field, format string, args ...any) {
		problems = append(problems, field+": "+fmt.Sprintf(format, args...))
	}
	if s.name == "" {
		problem("meta.name", "is missing")
	}
	if len(f.Steps) == 0 {
		problem("steps", "a workflow has at least one step")
	}
	for i, st := range f.Steps {
		at := "steps." + strconv.Itoa(i)
		step := kind.Step{Use: st.Use, With: st.With, Forbid: st.Forbid, Then: st.Then}
		if !resource.ValidCode(st.Use) {
			problem(at+".use", "%q is not the code of a skill or an agent", st.Use)
		}
		for j, w := range st.With {
			if !resource.ValidCode(w) {
				problem(at+".with."+strconv.Itoa(j), "%q is not a resource code", w)
			}
		}
		switch st.Then {
		case "":
			step.Then = kind.ThenContinue
		case kind.ThenContinue, kind.ThenConfirm:
		default:
			problem(at+".then", "%q is neither %s nor %s", st.Then, kind.ThenContinue, kind.ThenConfirm)
		}
		for j, c := range st.Checks {
			cat := at + ".checks." + strconv.Itoa(j)
			if strings.TrimSpace(c.Text) == "" {
				problem(cat+".text", "says nothing to check")
			}
			switch c.By {
			case kind.CheckByCommand:
				if strings.TrimSpace(c.Run) == "" {
					problem(cat+".run", "a check by command needs the command to run")
				}
			case kind.CheckByAI, kind.CheckByHuman:
				if c.Run != "" {
					problem(cat+".run", "only a check by command runs something; this one is by %s", c.By)
				}
			default:
				problem(cat+".by", "%q is none of ai, command and human", c.By)
			}
			step.Checks = append(step.Checks, kind.Check{Text: c.Text, By: c.By, Run: c.Run})
		}
		for j, x := range st.Forbid {
			if strings.TrimSpace(x) == "" {
				problem(at+".forbid."+strconv.Itoa(j), "is empty")
			}
		}
		s.steps = append(s.steps, step)
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return nil, fmt.Errorf("%s: %s", name, strings.Join(slices.Compact(problems), "; "))
	}
	return s, nil
}

// maxDescription is the longest description the Agent Skills convention allows, in characters.
const maxDescription = 1024

// description says what the workflow is for and when, which is what decides when an AI picks it.
func (s spec) description() string {
	d := s.name
	if s.summary != "" {
		d = s.summary
	}
	if s.when != "" {
		d += " — " + s.when
	}
	return truncate(d+" (workflow "+s.name+", "+strconv.Itoa(len(s.steps))+" steps)", maxDescription)
}

// asCommand delivers the workflow as a slash command the person starts by name.
func asCommand(sp kind.Spec, _ adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
	s := sp.(spec)
	body, err := s.body(opt, false)
	if err != nil {
		return nil, err
	}
	return []adapter.Artifact{adapter.Command{
		Resource:    s.ref,
		CmdName:     opt.Prefix + s.ref.Code,
		Description: s.description(),
		Body:        body,
	}}, nil
}

// asSkill delivers the workflow as a skill, for a tool without slash commands. The text is the
// command's, with a line on how the person starts it.
func asSkill(sp kind.Spec, _ adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
	s := sp.(spec)
	body, err := s.body(opt, true)
	if err != nil {
		return nil, err
	}
	name := opt.Prefix + s.ref.Code
	var b bytes.Buffer
	b.WriteString("---\nname: " + quote(name) + "\ndescription: " + quote(s.description()) + "\n---\n\n")
	b.WriteString(body)
	return []adapter.Artifact{adapter.SkillDir{
		Resource: s.ref,
		DirName:  name,
		Files:    []adapter.File{{Path: "SKILL.md", Data: b.Bytes()}},
		Degraded: AsSkill,
	}}, nil
}

// body is what the AI reads: not the steps, which the command line hands out one at a time, but
// how to get them and what to do with the checks.
func (s spec) body(opt kind.RenderOptions, asSkill bool) (string, error) {
	if opt.CLI == "" {
		return "", fmt.Errorf("%v: the name of the command-line program is missing from the render options", s.ref)
	}
	next := opt.CLI + " flow next " + s.ref.Code
	var b strings.Builder
	b.WriteString("# " + s.name + "\n\n")
	if s.summary != "" {
		b.WriteString(s.summary + "\n\n")
	}
	if asSkill {
		b.WriteString("This is a workflow. Follow it when the person asks for \"" + s.name + "\" or names `" + opt.Prefix + s.ref.Code + "`.\n\n")
	}
	fmt.Fprintf(&b, "The workflow has %d steps. You do not get them all at once: the command line hands them out one at a time, and only once the checks of the step before have passed.\n\n", len(s.steps))
	b.WriteString("1. In the project's directory, run:\n\n")
	b.WriteString("   ```sh\n   " + next + "\n   ```\n\n")
	b.WriteString("   It prints the step: which skill or agent does it, what else it may use, what must not be done, and the checks at its end.\n")
	b.WriteString("2. Do that step, and only that step.\n")
	b.WriteString("3. Run `" + next + "` again. It runs the checks that are commands itself. For a check you make, add `--evidence \"<what shows it holds>\"`. For a check the person makes, or when the step says to wait for the person, ask them and add `--confirmed` only after they have said yes in this conversation.\n")
	b.WriteString("4. When it says a check did not pass, put it right and run it again. Never go on to the next step without it.\n\n")
	b.WriteString("When it says the workflow is done, tell the person. To start again from the first step, run `" + next + " --restart`.\n")
	if !asSkill {
		b.WriteString("\nWhat the person gave you for this workflow: $ARGUMENTS\n")
	}
	return b.String(), nil
}

// quote writes s as a YAML scalar in double quotes. A JSON string is one.
func quote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

// truncate shortens s to at most n characters, ending it with an ellipsis when it had to cut.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
