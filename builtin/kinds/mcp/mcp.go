// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package mcp

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"regexp"
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

// The ways a server is delivered (the "delivery" field).
const (
	Native   = "native"    // into the tool's MCP configuration
	OnDemand = "on-demand" // as a skill that calls the server through the command line
)

// The transports a server can have (server.type).
const (
	typeStdio = "stdio"
	typeHTTP  = "http"
	typeSSE   = "sse"
)

// AsSkill is the message key of the explanation for a server that is meant to be connected
// natively and reaches a tool as a skill instead.
const AsSkill = "mcp.as_skill"

// New returns the MCP server kind.
func New() kind.Kind { return mcpKind{} }

type mcpKind struct{}

func (mcpKind) ID() string          { return resource.KindMCP }
func (mcpKind) Layout() kind.Layout { return kind.Layout{Dir: "mcp", File: true, Ext: ".yaml"} }
func (mcpKind) Schema() []byte      { return slices.Clone(schemaJSON) }

// Forms: natively where the tool takes the server's transport; as the skill of on-demand
// delivery when the server is meant so; as that skill, marked as a fallback, where a server
// meant to be connected natively cannot be.
func (mcpKind) Forms() []kind.Form {
	return []kind.Form{
		{Needs: kind.Needs{MCP: true}, Render: asServer},
		{Needs: kind.Needs{Skills: true}, Render: asOnDemandSkill},
		{Needs: kind.Needs{Skills: true}, Degraded: AsSkill, Render: asFallbackSkill},
	}
}

// spec is a parsed MCP server.
type spec struct {
	ref                 resource.Ref
	name, summary, when string // from meta, for the skill
	transport           string
	command             string
	args                []string
	env                 map[string]adapter.Value
	url                 string
	headers             map[string]adapter.Value
	onDemand            bool
	tools               []tool
	secrets             []string
}

type tool struct {
	Name    string `yaml:"name"`
	Summary string `yaml:"summary"`
	Writes  bool   `yaml:"writes"`
}

// Secrets implements kind.SecretUser: the secrets server.env and server.headers refer to.
func (s spec) Secrets() []string { return slices.Clone(s.secrets) }

var _ kind.SecretUser = spec{}

// file is mcp/<code>.yaml as written.
type file struct {
	Meta struct {
		Name    string `yaml:"name"`
		Summary string `yaml:"summary"`
		When    string `yaml:"when"`
	} `yaml:"meta"`
	Server struct {
		Type    string            `yaml:"type"`
		Command string            `yaml:"command"`
		Args    []string          `yaml:"args"`
		Env     map[string]string `yaml:"env"`
		URL     string            `yaml:"url"`
		Headers map[string]string `yaml:"headers"`
	} `yaml:"server"`
	Delivery string `yaml:"delivery"`
	Tools    []tool `yaml:"tools"`
}

// secretRef is a reference to a personal secret in a value: ${secret:NAME}.
var secretRef = regexp.MustCompile(`\$\{secret:([^}]*)\}`)

// secretMark is how every reference starts. Text that has it anywhere a reference is not
// allowed is refused, also when the reference is malformed.
const secretMark = "${secret:"

// Parse reads mcp/<code>.yaml and checks what the schema cannot: where secrets may be referred
// to and how, and which fields fit the transport. Every problem is reported with its field.
func (mcpKind) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	name := ref.Code + ".yaml"
	data, err := fs.ReadFile(files, name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%s is not YAML: %w", name, err)
	}
	var problems []string
	walkScalars(&root, nil, func(path []string, isKey bool, value string) {
		if !strings.Contains(value, secretMark) || (!isKey && secretAllowed(path)) {
			return
		}
		problems = append(problems, fmt.Sprintf("%s: a ${secret:…} may only be in the values of server.env and server.headers; "+
			"anywhere else it would end up in process lists, logs or the tool's configuration as text", strings.Join(path, ".")))
	})

	var f file
	if err := root.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	s := spec{
		ref:       ref,
		name:      f.Meta.Name,
		summary:   f.Meta.Summary,
		when:      f.Meta.When,
		transport: f.Server.Type,
		command:   f.Server.Command,
		args:      f.Server.Args,
		url:       f.Server.URL,
		tools:     f.Tools,
	}
	if s.name == "" {
		problems = append(problems, "meta.name: is missing")
	}
	switch f.Delivery {
	case "", Native:
	case OnDemand:
		s.onDemand = true
	default:
		problems = append(problems, fmt.Sprintf("delivery: %q is neither %s nor %s", f.Delivery, Native, OnDemand))
	}

	// What belongs to the other transport is a mistake, not something to ignore: the server
	// would not be what its author thinks.
	notFor := func(field string, set bool) {
		if set {
			problems = append(problems, fmt.Sprintf("server.%s: the server's type is %s, which has no %s", field, s.transport, field))
		}
	}
	switch s.transport {
	case typeStdio:
		if s.command == "" {
			problems = append(problems, "server.command: a stdio server needs the program to start")
		}
		notFor("url", f.Server.URL != "")
		notFor("headers", f.Server.Headers != nil)
	case typeHTTP, typeSSE:
		if u, err := url.Parse(s.url); s.url == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			problems = append(problems, fmt.Sprintf("server.url: %q is not an http or https address", s.url))
		}
		notFor("command", f.Server.Command != "")
		notFor("args", f.Server.Args != nil)
		notFor("env", f.Server.Env != nil)
	default:
		problems = append(problems, fmt.Sprintf("server.type: %q is none of stdio, http and sse", s.transport))
	}

	var secrets []string
	values := func(field string, in map[string]string) map[string]adapter.Value {
		if len(in) == 0 {
			return nil
		}
		out := make(map[string]adapter.Value, len(in))
		for _, k := range slices.Sorted(maps.Keys(in)) {
			v, err := parseValue(in[k])
			if err != nil {
				problems = append(problems, fmt.Sprintf("server.%s.%s: %v", field, k, err))
				continue
			}
			if v.Secret != "" && !slices.Contains(secrets, v.Secret) {
				secrets = append(secrets, v.Secret)
			}
			out[k] = v
		}
		return out
	}
	s.env = values("env", f.Server.Env)
	s.headers = values("headers", f.Server.Headers)
	slices.Sort(secrets)
	s.secrets = secrets

	var seen []string
	for i, t := range s.tools {
		switch {
		case strings.TrimSpace(t.Name) == "":
			problems = append(problems, fmt.Sprintf("tools.%d.name: is missing", i))
		case slices.Contains(seen, t.Name):
			problems = append(problems, fmt.Sprintf("tools.%d.name: the tool %q is listed twice", i, t.Name))
		}
		seen = append(seen, t.Name)
	}
	if s.onDemand && len(s.tools) == 0 {
		problems = append(problems, "tools: a server delivered on demand needs its tools listed; they are all the AI learns about it")
	}

	if len(problems) > 0 {
		slices.Sort(problems)
		return nil, fmt.Errorf("%s: %s", name, strings.Join(slices.Compact(problems), "; "))
	}
	return s, nil
}

// secretAllowed reports whether a value at path may refer to a secret: the value of one
// environment variable or one header of the server.
func secretAllowed(path []string) bool {
	return len(path) == 3 && path[0] == "server" && (path[1] == "env" || path[1] == "headers")
}

// walkScalars calls fn for every scalar of a YAML document, keys included, with the path to it.
func walkScalars(n *yaml.Node, path []string, fn func(path []string, isKey bool, value string)) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walkScalars(c, path, fn)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			child := append(slices.Clone(path), key.Value)
			fn(child, true, key.Value)
			walkScalars(n.Content[i+1], child, fn)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			walkScalars(c, append(slices.Clone(path), strconv.Itoa(i)), fn)
		}
	case yaml.ScalarNode:
		fn(path, false, n.Value)
	}
}

// parseValue reads one value of server.env or server.headers. A value is a literal, or a
// literal followed by one ${secret:NAME}: that is what every tool can be given ("Bearer " and
// a secret), and what adapter.Value holds.
func parseValue(v string) (adapter.Value, error) {
	refs := secretRef.FindAllStringSubmatchIndex(v, -1)
	switch {
	case len(refs) == 0 && strings.Contains(v, secretMark):
		return adapter.Value{}, errors.New("a ${secret:NAME} is not closed with }")
	case len(refs) == 0:
		return adapter.Value{Literal: v}, nil
	case len(refs) > 1:
		return adapter.Value{}, errors.New("refers to more than one secret; a value holds one, at its end")
	}
	ref := refs[0]
	if ref[1] != len(v) {
		return adapter.Value{}, fmt.Errorf("the secret has to be at the end of the value, as in \"Bearer ${secret:NAME}\"; %q cannot follow it", v[ref[1]:])
	}
	name := v[ref[2]:ref[3]]
	if !resource.ValidSecretName(name) {
		return adapter.Value{}, fmt.Errorf("%q is not a secret name: an upper-case letter, then 1 to 63 upper-case letters, digits and underscores", name)
	}
	literal := v[:ref[0]]
	if strings.Contains(literal, secretMark) {
		return adapter.Value{}, errors.New("a ${secret:NAME} is not closed with }")
	}
	return adapter.Value{Literal: literal, Secret: name}, nil
}

// asServer delivers the server into the tool's MCP configuration: when it is meant to be
// connected natively and the tool takes its transport.
func asServer(sp kind.Spec, caps adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
	s := sp.(spec)
	srv := adapter.MCPServer{Resource: s.ref, EntryName: opt.Prefix + s.ref.Code}
	switch {
	case s.onDemand:
		return nil, kind.ErrNotApplicable
	case s.transport == typeStdio && caps.MCP.Stdio:
		srv.Transport = adapter.Stdio
		srv.Command = s.command
		srv.Args = slices.Clone(s.args)
		srv.Env = maps.Clone(s.env)
	case s.transport == typeHTTP && caps.MCP.HTTP:
		srv.Transport = adapter.HTTP
		srv.URL = s.url
		srv.Headers = maps.Clone(s.headers)
	default:
		// The tool does not take this transport. The artifacts have no SSE transport at all:
		// an SSE server reaches tools through the skill.
		return nil, kind.ErrNotApplicable
	}
	return []adapter.Artifact{srv}, nil
}

func asOnDemandSkill(sp kind.Spec, _ adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
	s := sp.(spec)
	if !s.onDemand {
		return nil, kind.ErrNotApplicable
	}
	return asSkill(s, opt, "")
}

func asFallbackSkill(sp kind.Spec, _ adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
	s := sp.(spec)
	if s.onDemand || len(s.tools) == 0 {
		// Without its tools listed the skill could tell the AI nothing it can use.
		return nil, kind.ErrNotApplicable
	}
	return asSkill(s, opt, AsSkill)
}

// maxDescription is the longest description the Agent Skills convention allows, in characters.
const maxDescription = 1024

// asSkill writes the skill that tells the AI how to call the server's tools on demand. It holds
// nothing about how the server is reached, and no secret: the command line knows those.
func asSkill(s spec, opt kind.RenderOptions, degraded string) ([]adapter.Artifact, error) {
	if opt.CLI == "" {
		return nil, fmt.Errorf("%v: the name of the command-line program is missing from the render options", s.ref)
	}
	name := opt.Prefix + s.ref.Code
	call := opt.CLI + " mcp call " + s.ref.Code

	// What the server is for decides when the AI loads the skill, so it comes first.
	description := s.name
	if s.summary != "" {
		description = s.summary
	}
	if s.when != "" {
		description += " — " + s.when
	}
	description += " (MCP server " + s.name + ", called through " + call + ")"

	var b bytes.Buffer
	b.WriteString("---\nname: " + quote(name) + "\ndescription: " + quote(truncate(description, maxDescription)) + "\n---\n\n")
	b.WriteString("# " + s.name + "\n\n")
	if s.summary != "" {
		b.WriteString(s.summary + "\n\n")
	}
	if s.when != "" {
		b.WriteString("When: " + s.when + "\n\n")
	}
	if degraded == "" {
		b.WriteString("This MCP server is used on demand: it is not connected to this tool.")
	} else {
		b.WriteString("This tool cannot connect to this MCP server directly.")
	}
	b.WriteString(" Call one of its tools by running this command in a shell, with the tool's arguments as a JSON object:\n\n")
	b.WriteString("```sh\n" + call + " <tool> '<arguments as JSON>'\n```\n\n")
	b.WriteString("The command prints what the tool returns. It connects to the server and supplies any personal secrets the server needs; never put a secret on the command line.\n\n")
	b.WriteString("## Tools\n\n")
	for _, t := range s.tools {
		b.WriteString("- `" + t.Name + "`")
		if t.Summary != "" {
			b.WriteString(": " + t.Summary)
		}
		if t.Writes {
			b.WriteString(" (changes data: ask the person before calling it)")
		}
		b.WriteString("\n")
	}
	return []adapter.Artifact{adapter.SkillDir{
		Resource: s.ref,
		DirName:  name,
		Files:    []adapter.File{{Path: "SKILL.md", Data: b.Bytes()}},
		Degraded: degraded,
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
