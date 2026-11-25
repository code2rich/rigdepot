// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package agent

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v4"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
)

// The files of an agent's directory that are about the agent and are not delivered as they are.
const (
	// File describes the agent: its code, entry, own skills, what it binds, what stays local.
	File = "agent.yaml"
	// DefaultEntry is the file with the role's instructions when agent.yaml names none.
	DefaultEntry = "AGENT.md"
	metaFile     = "meta.yaml"
	// skillFile is what makes a directory a skill. The directory the agent is delivered in gets
	// one written by this kind, so the agent's own directory must not have one at its top.
	skillFile = "SKILL.md"
	// skillsDir holds the agent's own skills.
	skillsDir = "skills"
)

// AsSkill is the message key of the explanation for an agent that reaches a tool without
// sub-agents as a skill.
const AsSkill = "agent.as_skill"

//go:embed schema.json
var schemaJSON []byte

// New returns the agent kind.
func New() kind.Kind { return agentKind{} }

type agentKind struct{}

func (agentKind) ID() string          { return resource.KindAgent }
func (agentKind) Layout() kind.Layout { return kind.Layout{Dir: "agents"} }
func (agentKind) Schema() []byte      { return slices.Clone(schemaJSON) }

// Forms: a sub-agent, with a skill directory for its own skills and local directories when it
// has any; a sub-agent alone for a tool without skills, when it needs no directory; and in a tool
// without sub-agents, a skill that is the whole agent.
func (agentKind) Forms() []kind.Form {
	return []kind.Form{
		{Needs: kind.Needs{Agents: true, Skills: true}, Render: asSubAgent},
		{Needs: kind.Needs{Agents: true}, Render: asBareSubAgent},
		{Needs: kind.Needs{Skills: true}, Degraded: AsSkill, Render: asSkill},
	}
}

// spec is a parsed agent.
type spec struct {
	ref                 resource.Ref
	name, summary, when string
	// instructions is the content of the entry file.
	instructions string
	// skills are the agent's own skills, with their descriptions.
	skills []ownSkill
	binds  []string
	mcp    []string
	local  []string
	// files are delivered in the agent's directory as they are: its skills, the seeds of its
	// local directories, references, .env.example. Not agent.yaml, meta.yaml or the entry.
	files []adapter.File
}

type ownSkill struct{ name, description string }

var (
	_ kind.Referrer    = spec{}
	_ kind.LocalKeeper = spec{}
)

// Refs implements kind.Referrer: what the agent binds and the MCP servers it depends on.
func (s spec) Refs() []string {
	out := slices.Concat(s.binds, s.mcp)
	slices.Sort(out)
	return slices.Compact(out)
}

// Local implements kind.LocalKeeper.
func (s spec) Local() []string { return slices.Clone(s.local) }

// needsDir reports whether the agent needs a directory of its own besides its instructions.
func (s spec) needsDir() bool {
	return len(s.files) > 0 || len(s.local) > 0
}

type agentFile struct {
	ID     string   `yaml:"id"`
	Entry  string   `yaml:"entry"`
	Skills []string `yaml:"skills"`
	Binds  []string `yaml:"binds"`
	MCP    []string `yaml:"mcp"`
	Local  []string `yaml:"local"`
}

type metaDoc struct {
	Name    string `yaml:"name"`
	Summary string `yaml:"summary"`
	When    string `yaml:"when"`
}

// Parse reads agents/<code>/: agent.yaml against the schema and the directory, the entry, the
// own skills, the local directories. Every problem is reported, each with its file and field.
func (agentKind) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	data, err := fs.ReadFile(files, File)
	if err != nil {
		return nil, fmt.Errorf("%s: an agent needs one: %w", File, err)
	}
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s is not YAML: %w", File, err)
	}
	if err := validate(doc); err != nil {
		return nil, err
	}
	var a agentFile
	if err := yaml.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("%s: %w", File, err)
	}
	var m metaDoc
	if raw, err := fs.ReadFile(files, metaFile); err == nil {
		if err := yaml.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", metaFile, err)
		}
	}

	var problems []string
	problem := func(field, format string, args ...any) {
		problems = append(problems, field+": "+fmt.Sprintf(format, args...))
	}
	if a.ID != ref.Code {
		problem("id", "is %q; it has to equal the directory name %q", a.ID, ref.Code)
	}
	entry := a.Entry
	if entry == "" {
		entry = DefaultEntry
	}
	s := spec{ref: ref, name: m.Name, summary: m.Summary, when: m.When, binds: a.Binds, mcp: a.MCP}
	if m.Name == "" {
		s.name = ref.Code
	}
	switch {
	case !cleanPath(entry):
		problem("entry", "%q is not a path inside the agent's directory, written with forward slashes", entry)
	case entry == File || entry == metaFile || entry == skillFile:
		problem("entry", "%q cannot be the entry; write the role's instructions in a file of their own, such as %s", entry, DefaultEntry)
	default:
		text, err := fs.ReadFile(files, entry)
		if err != nil {
			problem("entry", "%s cannot be read: %v", entry, err)
		} else if strings.TrimSpace(string(text)) == "" {
			problem(entry, "is empty; it holds the role's instructions")
		} else {
			s.instructions = strings.TrimPrefix(strings.ReplaceAll(string(text), "\r\n", "\n"), "\uFEFF")
		}
	}
	if _, err := fs.Stat(files, skillFile); err == nil {
		problem(skillFile, "an agent's directory has none at its top: the role's instructions are in its entry (%s), and the skill an agent is delivered as is written from them", DefaultEntry)
	}

	for _, l := range a.Local {
		clean := strings.TrimSuffix(l, "/")
		switch {
		case !cleanPath(clean):
			problem("local", "%q is not a directory inside the agent's directory, written with forward slashes", l)
		case clean == skillsDir || strings.HasPrefix(clean, skillsDir+"/") || clean == entry || clean == File || clean == metaFile:
			problem("local", "%q is part of what is delivered; a local directory is one of its own", l)
		case slices.Contains(s.local, clean):
			problem("local", "%q is listed twice", l)
		default:
			s.local = append(s.local, clean)
		}
	}
	slices.Sort(s.local)

	for _, name := range a.Skills {
		desc, err := skillDescription(files, name)
		if err != nil {
			problem(skillsDir+"/"+name, "%v", err)
			continue
		}
		s.skills = append(s.skills, ownSkill{name: name, description: desc})
	}
	if entries, err := fs.ReadDir(files, skillsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() && !slices.Contains(a.Skills, e.Name()) {
				problem(skillsDir+"/"+e.Name(), "is not listed under skills in %s; list it, or move it out of skills/", File)
			}
		}
	}

	err = fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == File || p == metaFile || p == entry {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(files, p)
		if err != nil {
			return err
		}
		s.files = append(s.files, adapter.File{Path: p, Data: data, Exec: info.Mode().Perm()&0o111 != 0})
		return nil
	})
	if err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return nil, fmt.Errorf("agent %s: %s", ref.Code, strings.Join(slices.Compact(problems), "; "))
	}
	return s, nil
}

// cleanPath reports whether p is a clean relative path inside the directory, with forward
// slashes.
func cleanPath(p string) bool {
	return p != "" && p != "." && fs.ValidPath(p) && !strings.Contains(p, "\\") && path.Clean(p) == p
}

// skillDescription reads skills/<name>/SKILL.md and returns the description in its front matter.
// Its name has to be the directory's, as for any skill.
func skillDescription(files fs.FS, name string) (string, error) {
	data, err := fs.ReadFile(files, skillsDir+"/"+name+"/"+skillFile)
	if err != nil {
		return "", fmt.Errorf("an own skill needs a %s: %w", skillFile, err)
	}
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\uFEFF"), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	front, _, ok2 := strings.Cut(rest, "\n---")
	if !ok || !ok2 {
		return "", fmt.Errorf("%s has no front matter", skillFile)
	}
	var fm struct{ Name, Description string }
	if err := yaml.Unmarshal([]byte(front), &fm); err != nil {
		return "", fmt.Errorf("the front matter of %s is not YAML: %w", skillFile, err)
	}
	if fm.Name != name {
		return "", fmt.Errorf("the name in %s is %q; it has to equal the directory name %q", skillFile, fm.Name, name)
	}
	if strings.TrimSpace(fm.Description) == "" {
		return "", fmt.Errorf("%s has no description", skillFile)
	}
	return fm.Description, nil
}

// maxDescription is the longest description the Agent Skills convention allows, in characters.
const maxDescription = 1024

func (s spec) description() string {
	d := s.name
	if s.summary != "" {
		d = s.summary
	}
	if s.when != "" {
		d += " — " + s.when
	}
	return truncate(d, maxDescription)
}

// asSubAgent delivers the agent as a sub-agent, and, when it has own skills, local directories or
// other files, a skill directory of the same name that holds them.
func asSubAgent(sp kind.Spec, _ adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
	s := sp.(spec)
	name := opt.Prefix + s.ref.Code
	body := s.instructions
	if s.needsDir() {
		body = withSection(body, "Your own files", "Your own skills and working directories are in the skill `"+name+"`. Load it at the start of your work; it says what is there.")
	}
	body = s.withBinds(body, opt)
	arts := []adapter.Artifact{adapter.SubAgent{Resource: s.ref, AgentName: name, Description: s.description(), Body: body}}
	if !s.needsDir() {
		return arts, nil
	}
	var b bytes.Buffer
	b.WriteString("---\nname: " + quote(name) + "\ndescription: " + quote(truncate("The own skills and working directories of the agent "+s.name+" (the sub-agent "+name+"). Load it when you work as that agent.", maxDescription)) + "\n---\n\n")
	b.WriteString("# " + s.name + ": own files\n\n")
	b.WriteString("These are the files of the agent " + s.name + ". If you are not working as that agent, hand the work to the sub-agent `" + name + "` instead of using them.\n")
	b.WriteString(s.filesSection())
	return []adapter.Artifact{arts[0], s.skillDir(name, b.Bytes(), "")}, nil
}

// asBareSubAgent is the sub-agent alone, for a tool without skills: only an agent that needs no
// directory fits it.
func asBareSubAgent(sp kind.Spec, _ adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
	s := sp.(spec)
	if s.needsDir() {
		return nil, kind.ErrNotApplicable
	}
	name := opt.Prefix + s.ref.Code
	return []adapter.Artifact{adapter.SubAgent{Resource: s.ref, AgentName: name, Description: s.description(), Body: s.withBinds(s.instructions, opt)}}, nil
}

// asSkill delivers the whole agent as one skill: its instructions, then its own files.
func asSkill(sp kind.Spec, _ adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
	s := sp.(spec)
	name := opt.Prefix + s.ref.Code
	var b bytes.Buffer
	b.WriteString("---\nname: " + quote(name) + "\ndescription: " + quote(truncate("Work as the agent "+s.name+": "+s.description(), maxDescription)) + "\n---\n\n")
	b.WriteString("This tool has no sub-agents, so the agent " + s.name + " is this skill. When the person asks for " + s.name + ", or the work is what it is for, follow the instructions below as that agent.\n\n")
	body := s.withBinds(s.instructions, opt)
	b.WriteString(strings.TrimRight(body, "\n") + "\n")
	if s.needsDir() {
		b.WriteString(s.filesSection())
	}
	return []adapter.Artifact{s.skillDir(name, b.Bytes(), AsSkill)}, nil
}

func (s spec) skillDir(name string, skillMD []byte, degraded string) adapter.SkillDir {
	files := make([]adapter.File, 0, len(s.files)+1)
	files = append(files, adapter.File{Path: skillFile, Data: skillMD})
	for _, f := range s.files {
		files = append(files, adapter.File{Path: f.Path, Data: bytes.Clone(f.Data), Exec: f.Exec})
	}
	return adapter.SkillDir{Resource: s.ref, DirName: name, Files: files, Degraded: degraded, Local: slices.Clone(s.local)}
}

// filesSection says what is in the agent's directory: its own skills and its local directories.
func (s spec) filesSection() string {
	var b strings.Builder
	if len(s.skills) > 0 {
		b.WriteString("\n## Own skills\n\nIn this skill's directory. Read the SKILL.md of the one that fits the work before you do it:\n\n")
		for _, sk := range s.skills {
			b.WriteString("- `" + skillsDir + "/" + sk.name + "/" + skillFile + "`: " + sk.description + "\n")
		}
	}
	if len(s.local) > 0 {
		b.WriteString("\n## Working directories\n\nIn this skill's directory. What you write into them stays on this computer: updates of the agent do not touch it.\n\n")
		for _, l := range s.local {
			b.WriteString("- `" + l + "/`\n")
		}
	}
	return b.String()
}

// withBinds adds what the agent works with to its instructions.
func (s spec) withBinds(body string, opt kind.RenderOptions) string {
	if len(s.binds) == 0 && len(s.mcp) == 0 {
		return body
	}
	var b strings.Builder
	if len(s.binds) > 0 {
		b.WriteString("Resources installed along with you; use them where they fit:\n\n")
		for _, c := range s.binds {
			b.WriteString("- `" + opt.Prefix + c + "`\n")
		}
	}
	if len(s.mcp) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("MCP servers you depend on: " + strings.Join(s.mcp, ", ") + ".\n")
	}
	return withSection(body, "What you work with", strings.TrimRight(b.String(), "\n"))
}

func withSection(body, title, text string) string {
	return strings.TrimRight(body, "\n") + "\n\n## " + title + "\n\n" + text + "\n"
}

var compiled = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, err
	}
	id, _ := doc.(map[string]any)["$id"].(string)
	c := jsonschema.NewCompiler()
	c.UseLoader(jsonschema.SchemeURLLoader{}) // nothing is ever fetched
	if err := c.AddResource(id, doc); err != nil {
		return nil, err
	}
	return c.Compile(id)
})

// validate checks agent.yaml against the schema. The error names every field that does not fit.
func validate(doc any) error {
	if _, ok := doc.(map[string]any); !ok {
		return fmt.Errorf("%s has to be a mapping with at least id", File)
	}
	js, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("%s cannot be read: %w", File, err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(js))
	if err != nil {
		return err
	}
	sch, err := compiled()
	if err != nil {
		return fmt.Errorf("the schema of the agent kind: %w", err)
	}
	err = sch.Validate(v)
	var verr *jsonschema.ValidationError
	if err == nil || !errors.As(err, &verr) {
		return err
	}
	var problems []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) > 0 {
			for _, c := range e.Causes {
				walk(c)
			}
			return
		}
		msg := ""
		if out := e.BasicOutput(); out.Error != nil {
			msg = out.Error.String()
		}
		if len(e.InstanceLocation) > 0 {
			msg = strings.Join(e.InstanceLocation, ".") + ": " + msg
		}
		problems = append(problems, msg)
	}
	walk(verr)
	slices.Sort(problems)
	return fmt.Errorf("%s does not fit: %s", File, strings.Join(slices.Compact(problems), "; "))
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
