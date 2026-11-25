// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adapter

import "github.com/code2rich/rigdepot/ext/resource"

// Artifact is something a tool can take, described without reference to any tool. Resource
// kinds produce artifacts and adapters consume them.
//
// The set is closed: only SkillDir, MCPServer, Command and SubAgent implement it, always as
// values, never as pointers. A new resource kind should render into these shapes, because then
// no adapter has to change. A new shape affects every adapter and is added here.
type Artifact interface {
	artifact()

	// Ref returns the resource, and the version of it, this artifact was rendered from.
	Ref() resource.Ref

	// Name returns the name the artifact gets inside the tool, with the product's prefix
	// already applied.
	Name() string
}

// SkillDir is a skill directory: a directory with at least a SKILL.md in it.
type SkillDir struct {
	Resource resource.Ref
	// DirName is the name of the directory, with the product's prefix.
	DirName string
	// Files is the content of the directory. Their paths are relative to it.
	Files []File
	// Degraded is empty for a resource that is a skill. Otherwise the resource is something
	// else delivered as a skill because the tool has no native form for it, and this is the
	// message key of the explanation, such as "workflow.as_skill".
	Degraded string
}

// MCPServer is one connection to an MCP server.
type MCPServer struct {
	Resource resource.Ref
	// EntryName is the name of the entry in the tool's configuration, with the product's
	// prefix.
	EntryName string
	Transport Transport

	// Command, Args and Env describe the process to start. Used with Stdio.
	Command string
	Args    []string
	Env     map[string]Value

	// URL and Headers describe the endpoint. Used with HTTP.
	URL     string
	Headers map[string]Value
}

// Command is a slash command: a prompt a person starts by name.
type Command struct {
	Resource resource.Ref
	// CmdName is the name of the command, with the product's prefix and without a slash.
	CmdName     string
	Description string
	// Body is the prompt, in Markdown.
	Body string
}

// SubAgent is the definition of a sub-agent: a role the tool can hand work to.
type SubAgent struct {
	Resource resource.Ref
	// AgentName is the name of the agent, with the product's prefix.
	AgentName   string
	Description string
	// Body is the agent's instructions, in Markdown.
	Body string
}

func (SkillDir) artifact()  {}
func (MCPServer) artifact() {}
func (Command) artifact()   {}
func (SubAgent) artifact()  {}

// Ref implements Artifact.
func (a SkillDir) Ref() resource.Ref { return a.Resource }

// Ref implements Artifact.
func (a MCPServer) Ref() resource.Ref { return a.Resource }

// Ref implements Artifact.
func (a Command) Ref() resource.Ref { return a.Resource }

// Ref implements Artifact.
func (a SubAgent) Ref() resource.Ref { return a.Resource }

// Name implements Artifact. It returns DirName.
func (a SkillDir) Name() string { return a.DirName }

// Name implements Artifact. It returns EntryName.
func (a MCPServer) Name() string { return a.EntryName }

// Name implements Artifact. It returns CmdName.
func (a Command) Name() string { return a.CmdName }

// Name implements Artifact. It returns AgentName.
func (a SubAgent) Name() string { return a.AgentName }

// File is one file of a tree.
type File struct {
	// Path is relative to the tree, with forward slashes.
	Path string
	Data []byte
	// Exec asks for the executable bit. It is ignored on Windows and is not part of a content
	// digest.
	Exec bool
}

// Transport is how an MCP server is reached.
type Transport string

const (
	Stdio Transport = "stdio" // a local process, spoken to over its standard input and output
	HTTP  Transport = "http"  // an HTTP endpoint
)

// Value is one configuration value of an MCP server: a literal, a personal secret, or a literal
// followed by a secret.
//
// An adapter never sees the value of a secret, only its name. It writes whatever the tool's own
// configuration offers for "take this from somewhere else", or reports the artifact in
// Plan.Skips with NeedsSecretSupport when the tool offers nothing.
type Value struct {
	// Literal is the whole value when Secret is empty. When Secret is set, it is the text in
	// front of the secret's value: "Bearer " for the usual Authorization header, or nothing.
	Literal string
	// Secret, when not empty, is the name of the personal secret whose value completes this
	// one.
	Secret string
}
