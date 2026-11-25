// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package knowledge

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
)

//go:embed schema.json
var schemaJSON []byte

// Git is the one source type there is: a directory of a Git repository.
const Git = "git"

// New returns the knowledge base kind.
func New() kind.Kind { return knowledgeKind{} }

type knowledgeKind struct{}

func (knowledgeKind) ID() string { return resource.KindKnowledge }
func (knowledgeKind) Layout() kind.Layout {
	return kind.Layout{Dir: "knowledge", File: true, Ext: ".yaml"}
}
func (knowledgeKind) Schema() []byte { return slices.Clone(schemaJSON) }

// Forms: a skill that tells the AI where the content is on this computer and how to search it.
// It is what a knowledge base is, not a fallback.
func (knowledgeKind) Forms() []kind.Form {
	return []kind.Form{{Needs: kind.Needs{Skills: true}, Render: asSkill}}
}

// spec is a parsed knowledge base: where its content is, and what it is about.
type spec struct {
	ref                           resource.Ref
	name, summary, when, category string
	src                           kind.GitSource
}

var _ kind.Pulled = spec{}

// Pull implements kind.Pulled.
func (s spec) Pull() kind.GitSource { return s.src }

type file struct {
	Meta struct {
		Name    string `yaml:"name"`
		Summary string `yaml:"summary"`
		When    string `yaml:"when"`
	} `yaml:"meta"`
	Source struct {
		Type     string `yaml:"type"`
		Repo     string `yaml:"repo"`
		Branch   string `yaml:"branch"`
		Dir      string `yaml:"dir"`
		Category string `yaml:"category"`
	} `yaml:"source"`
}

// scpLike is the short ssh form git takes: user@host:path.
var scpLike = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^/\\]`)

// Parse reads knowledge/<code>.yaml and checks the address, the branch and the directory the way
// the command line will use them.
func (knowledgeKind) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	name := ref.Code + ".yaml"
	data, err := fs.ReadFile(files, name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	src := f.Source
	s := spec{ref: ref, name: f.Meta.Name, summary: f.Meta.Summary, when: f.Meta.When, category: src.Category,
		src: kind.GitSource{URL: src.Repo, Ref: src.Branch, Dir: strings.Trim(src.Dir, "/")}}
	var problems []string
	problem := func(field, format string, args ...any) {
		problems = append(problems, field+": "+fmt.Sprintf(format, args...))
	}
	if s.name == "" {
		problem("meta.name", "is missing")
	}
	if src.Type != Git {
		problem("source.type", "%q is not a source this version reads; it reads %s", src.Type, Git)
	}
	if err := checkRepo(src.Repo); err != nil {
		problem("source.repo", "%v", err)
	}
	if b := src.Branch; b != "" && (strings.HasPrefix(b, "-") || strings.ContainsAny(b, " \t\n\\:~^?*[") || strings.Contains(b, "..")) {
		problem("source.branch", "%q is not a branch, tag or commit", b)
	}
	if d := s.src.Dir; d != "" && (!fs.ValidPath(d) || strings.Contains(d, "\\") || path.Clean(d) != d) {
		problem("source.dir", "%q is not a directory inside the repository, written with forward slashes", src.Dir)
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return nil, fmt.Errorf("%s: %s", name, strings.Join(problems, "; "))
	}
	return s, nil
}

// checkRepo accepts what the command line will fetch from: https and ssh, without a password.
// Everyone reads the repository with their own access, so a password in the address would be a
// secret in the resource repository.
func checkRepo(repo string) error {
	if scpLike.MatchString(repo) {
		return nil
	}
	u, err := url.Parse(repo)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "ssh") {
		return fmt.Errorf("%q is not an https or ssh address of a Git repository", repo)
	}
	if _, set := u.User.Password(); set {
		return fmt.Errorf("the address carries a password; everyone reads the repository with their own access")
	}
	return nil
}

// maxDescription is the longest description the Agent Skills convention allows, in characters.
const maxDescription = 1024

// asSkill writes the skill that points the AI at the content on this computer. It holds no
// content: the command line fetches that with the person's own access.
func asSkill(sp kind.Spec, _ adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
	s := sp.(spec)
	if opt.Data == "" {
		return nil, fmt.Errorf("%v: the local data directory is missing from the render options", s.ref)
	}
	if opt.CLI == "" {
		return nil, fmt.Errorf("%v: the name of the command-line program is missing from the render options", s.ref)
	}
	dir := strings.TrimSuffix(opt.Data, "/") + "/knowledge/" + s.ref.Code + "/"
	name := opt.Prefix + s.ref.Code

	description := s.name
	if s.summary != "" {
		description = s.summary
	}
	if s.when != "" {
		description += " — " + s.when
	}
	description += " (knowledge base " + s.name
	if s.category != "" {
		description += ", " + s.category
	}
	description += ")"

	var b bytes.Buffer
	b.WriteString("---\nname: " + quote(name) + "\ndescription: " + quote(truncate(description, maxDescription)) + "\n---\n\n")
	b.WriteString("# " + s.name + "\n\n")
	if s.summary != "" {
		b.WriteString(s.summary + "\n\n")
	}
	if s.category != "" {
		b.WriteString("Category: " + s.category + "\n\n")
	}
	if s.when != "" {
		b.WriteString("When: " + s.when + "\n\n")
	}
	b.WriteString("The documents of this knowledge base are on this computer, in:\n\n")
	b.WriteString("```text\n" + dir + "\n```\n\n")
	b.WriteString("Search them (for example with `grep -rn` or `rg`) and read the files that match before you answer or change anything they cover. Say which file each piece of guidance comes from. Do not copy them anywhere else.\n\n")
	b.WriteString("If the directory is missing or empty, the person has not fetched it yet: ask them to run `" + opt.CLI + " sync`.\n")
	return []adapter.Artifact{adapter.SkillDir{
		Resource: s.ref,
		DirName:  name,
		Files:    []adapter.File{{Path: "SKILL.md", Data: b.Bytes()}},
	}}, nil
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
