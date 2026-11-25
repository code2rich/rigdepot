// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adapter

// Profile is the declarative part of an adapter: everything about a tool that can be said as
// data. A tool that is shaped like the others needs nothing but a profile; code is written only
// for what a profile cannot express.
//
// A profile is usually a profile.yaml embedded next to the adapter and read with package
// profileyaml, but it is an ordinary struct and can be written as a literal. The JSON names
// below are the field names of profile.yaml, and schema/adapter/v1/profile.schema.json
// describes the same shape for editors.
//
// The server reads profiles too: the table of where each tool keeps what is rendered from them.
//
// # Path templates
//
// Wherever a profile names a directory or file, the value is a template: "{name}" stands for
// the root of that name from Roots, as resolved on the person's computer, and a path that
// starts with "~/" lies in the home directory. Forward slashes on every platform.
type Profile struct {
	// ID is the adapter's identifier, the same as Adapter.ID.
	ID string `json:"id"`
	// Name is the tool's own name, such as "Claude Code". It is a proper name and is not
	// translated.
	Name string `json:"name"`

	// Detect says how to tell whether the tool is installed.
	Detect Detect `json:"detect"`

	// Roots names the directories everything else is relative to, and how to find each on a
	// given computer.
	Roots map[string]Root `json:"roots,omitempty"`

	// Verified records how the facts in this profile were checked against the real tool. A
	// profile without it is inert: the adapter reports no capabilities and nothing is ever
	// written to the tool. Guesses about another tool's configuration are not acted on.
	Verified *Verification `json:"verified,omitempty"`

	// Skills, Commands, Agents and MCP say where each kind of artifact goes. A nil field means
	// the tool has no such place, and resource kinds fall back to another form.
	Skills   *SkillsTarget `json:"skills,omitempty"`
	Commands *FilesTarget  `json:"commands,omitempty"`
	Agents   *FilesTarget  `json:"agents,omitempty"`
	MCP      *MCPTarget    `json:"mcp,omitempty"`

	// AlsoReads lists directories of other tools that this tool reads as well. When another
	// tool's plan already puts a resource into such a directory, the core drops this tool's
	// own copy, so that a resource does not show up twice in one tool.
	AlsoReads []SharedDir `json:"alsoReads,omitempty"`

	// Credentials lists the names of files that hold the tool's credentials. A preview never
	// copies a file with one of these names into its temporary directory, even when the file
	// is the declared target of an operation.
	Credentials []string `json:"credentials,omitempty"`
}

// Detect describes how a tool is found. The tool counts as installed when one of Bins is on the
// PATH or one of Dirs exists.
type Detect struct {
	// Bins lists the names of the tool's executable.
	Bins []string `json:"bins,omitempty"`
	// Dirs lists path templates of directories that only exist when the tool has been used.
	Dirs []string `json:"dirs,omitempty"`
	// Version says how to ask the tool for its version. Nil means the version stays unknown.
	Version *VersionProbe `json:"version,omitempty"`
}

// VersionProbe is a command that prints the tool's version and how to find the version in what
// it prints.
type VersionProbe struct {
	// Run is the command and its arguments, such as ["opencode", "--version"].
	Run []string `json:"run"`
	// Match is a regular expression (RE2 syntax) whose first group is the version.
	Match string `json:"match"`
}

// Root says where one of a tool's directories is. If Env names an environment variable and that
// variable holds an absolute path, the root is that path with Join appended; otherwise it is
// Default. A relative path in the variable does not count, because it would be relative to
// wherever the tool happened to be started.
type Root struct {
	// Env is the environment variable that moves this root, or "".
	Env string `json:"env,omitempty"`
	// Join is appended to the variable's value, for variables that name a parent directory.
	Join string `json:"join,omitempty"`
	// Default is where the root is when the variable is not set, such as "~/.config/opencode".
	Default string `json:"default"`
}

// Verification records that a profile was checked against the real tool, in enough detail for
// anyone to repeat the check.
type Verification struct {
	// Version is the version of the tool that was checked.
	Version string `json:"version"`
	// OS lists the operating systems it was checked on, as GOOS values.
	OS []string `json:"os"`
	// At is the date of the check, as YYYY-MM-DD.
	At string `json:"at"`
	// How says what was done, such as "ran the tool's own mcp add in a temporary home directory
	// and read the file it wrote".
	How string `json:"how"`
}

// SkillsTarget says where a tool keeps skills.
type SkillsTarget struct {
	// Dir is the path template of the directory that holds one entry per skill.
	Dir string `json:"dir"`
	// Layout says what an entry is. LayoutDir is the only layout today.
	Layout string `json:"layout"`
}

// LayoutDir is the skill layout in which every skill is a directory of its own.
const LayoutDir = "dir"

// FilesTarget says where a tool keeps artifacts that are a single file each: slash commands and
// sub-agents.
type FilesTarget struct {
	// Dir is the path template of the directory.
	Dir string `json:"dir"`
	// Ext is the file extension with its dot, such as ".md".
	Ext string `json:"ext"`
}

// MCPTarget says where a tool keeps its MCP servers.
type MCPTarget struct {
	// Files lists path templates of the configuration file, for tools that accept more than
	// one name. The one that exists is used; when none exists the last one is created.
	Files  []string `json:"files"`
	Format Format   `json:"format"`
	// Path is the key under which the servers are listed, one element per level, such as
	// ["mcpServers"]. Each server becomes one more key below it.
	Path []string `json:"path"`
}

// SharedDir is a directory of another tool that a tool reads as well.
type SharedDir struct {
	// Kind says what the tool reads from the directory. "skill" is the only kind today.
	Kind string `json:"kind"`
	// Dir is the directory, such as "~/.claude/skills".
	Dir string `json:"dir"`
}
