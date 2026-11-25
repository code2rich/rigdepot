// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adapter

import "github.com/code2rich/rigdepot/ext/resource"

// Op is one thing to be written, as data. An adapter returns operations; the core's executor
// applies them, to a temporary copy of the home directory for a preview and to the real one
// after the person agreed, and it is the executor that can undo them.
//
// The set is closed: only WriteTree, WriteFile, SetKey and Exec implement it, always as values,
// never as pointers. Paths are written as described in the package documentation.
//
// The executor checks every operation before it runs it: the target carries the product's
// prefix, lies inside a root the tool's profile declares, and is still in the state the plan
// saw. An operation that fails the first two checks is a defect in the adapter and is reported
// as one.
type Op interface {
	op()

	// Ref returns the resource this operation delivers.
	Ref() resource.Ref
}

// WriteTree creates a directory of ours or replaces it as a whole. Undoing it removes the
// directory.
type WriteTree struct {
	Resource resource.Ref
	// Dir is the directory to create, such as "~/.claude/skills/<prefix>unit-testing". Its
	// last element must carry the product's prefix.
	Dir string
	// Files is the complete content of the directory.
	Files []File
	// Local lists directories inside Dir, relative to it with forward slashes, whose content
	// belongs to the person's computer (SkillDir.Local). Replacing the directory keeps what is
	// in them, and they do not count towards its digest; undoing the operation keeps a copy of
	// what is in them before the directory goes.
	Local []string
}

// WriteFile creates a file of ours or replaces it. Undoing it removes the file.
type WriteFile struct {
	Resource resource.Ref
	// Path is the file to create. Its last element must carry the product's prefix.
	Path string
	Data []byte
}

// SetKey sets one key in a configuration file that belongs to the tool, leaving every other
// byte of the file as it was. Undoing it removes the key again.
//
// This changes the person's global configuration: the first time, it needs their consent, and
// the file is backed up before it is touched.
type SetKey struct {
	Resource resource.Ref
	// File is the configuration file, such as "~/.claude.json".
	File   string
	Format Format
	// Path is the key, one element per level, such as ["mcpServers", "<prefix>jira"]. Its last
	// element must carry the product's prefix or be the name of the product's single MCP entry.
	Path []string
	// Value is what the key is set to, in the types encoding/json decodes into plus integers:
	// string, bool, int, int64, float64, []any and map[string]any, nested as needed. Nothing
	// else is accepted, in particular not nil, not []string and not a struct, so that the same
	// value always serialises to the same bytes.
	Value any
}

// Exec installs something with the tool's own command, for tools that have a plugin mechanism.
// Like SetKey it needs the person's consent.
//
// A preview does not run the command, because a tool's command may use the network and cannot
// be confined to a temporary directory. The preview shows Argv and Effects and says that this
// step was not executed.
type Exec struct {
	Resource resource.Ref
	// Argv is the command and its arguments. It is run as it is, without a shell.
	Argv []string
	// Undo is the command that reverses Argv, run the same way.
	Undo []string
	// Effects lists, as message keys, what the command will do, for the person to read before
	// agreeing.
	Effects []string
}

func (WriteTree) op() {}
func (WriteFile) op() {}
func (SetKey) op()    {}
func (Exec) op()      {}

// Ref implements Op.
func (o WriteTree) Ref() resource.Ref { return o.Resource }

// Ref implements Op.
func (o WriteFile) Ref() resource.Ref { return o.Resource }

// Ref implements Op.
func (o SetKey) Ref() resource.Ref { return o.Resource }

// Ref implements Op.
func (o Exec) Ref() resource.Ref { return o.Resource }

// Format is the syntax of a configuration file.
type Format string

const (
	JSON  Format = "json"  // strict JSON: no comments, no trailing commas
	JSONC Format = "jsonc" // JSON with comments and trailing commas, both preserved
	TOML  Format = "toml"
)

// Valid reports whether f is one of the formats the executor can edit.
func (f Format) Valid() bool {
	return f == JSON || f == JSONC || f == TOML
}
