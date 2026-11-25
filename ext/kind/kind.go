// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package kind

import (
	"errors"
	"io/fs"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
)

// Kind describes one kind of resource. An implementation is stateless and does no input or
// output of its own: it reads the files it is given and returns values.
type Kind interface {
	// ID returns the stable identifier of the kind, in lower case with hyphens, such as
	// "skill" or "mcp". It is what resource.Ref.Kind holds.
	ID() string

	// Layout says where resources of this kind are in a repository and whether each is a
	// directory or a single file.
	Layout() Layout

	// Schema returns the JSON Schema (draft 2020-12) of the kind's own file: the single file
	// of a file-shaped kind, or the structured part of a directory-shaped one, such as the
	// front matter of SKILL.md. The schema describes shape only: types, required fields,
	// enumerations and patterns. What needs a second file to check belongs in Parse or in a
	// check rule.
	//
	// The original lives next to the kind's code. A copy of every kind's schema is collected
	// under schema/repo/v1/kinds/ for publishing.
	Schema() []byte

	// Parse reads one resource and validates what only this kind can validate. files is laid
	// out as resource.Bundle.Files describes: the resource's directory, or a directory holding
	// the resource's one file. ref.Kind is this kind's ID.
	//
	// The returned Spec is handed back, unchanged, to the Render functions of this kind's
	// forms. A non-nil error means the resource cannot be used; it is never delivered half
	// parsed.
	Parse(ref resource.Ref, files fs.FS) (Spec, error)

	// Forms lists the forms a resource of this kind can be delivered in, in order of
	// preference. The order is the fallback rule.
	Forms() []Form
}

// Layout is the place of a kind in the resource repository. The top-level directory decides the
// kind, and the name below it, without Ext, is the resource's code.
type Layout struct {
	// Dir is the top-level directory, such as "skills" or "mcp". No two kinds share one.
	Dir string
	// File is false when every resource is a directory (skills/<code>/) and true when every
	// resource is a single file (mcp/<code>.yaml).
	File bool
	// Ext is the extension of the file with its dot, such as ".yaml". Empty when File is false.
	Ext string
}

// Spec is one parsed and validated resource. Its concrete type belongs to the kind that
// returned it from Parse; the core passes it on without looking inside, and only that kind's
// Render functions convert it back.
type Spec any

// Form is one way of delivering a resource: what the tool has to support for it, and how the
// resource becomes artifacts.
type Form struct {
	// Needs is what a tool must be capable of for this form to be considered.
	Needs Needs

	// Degraded is empty for a form that delivers the resource as what it is. For a fallback it
	// is the message key of the explanation, such as "workflow.as_skill"; the explanation
	// travels with the resource into the plan, the status and the install report, so people
	// see that a workflow reached this tool as a skill.
	Degraded string

	// Render turns the resource into artifacts for a tool with the given capabilities. The
	// name of every artifact must start with opt.Prefix; a resource that becomes one artifact
	// names it opt.Prefix followed by the resource's code.
	//
	// Render returns ErrNotApplicable when the form does not fit this particular resource or
	// tool after all (an MCP server marked for on-demand delivery has no native form; a stdio
	// server does not fit a tool that only takes HTTP). The core then tries the next form. Any
	// other error fails the resource for this tool.
	Render func(s Spec, caps adapter.Capabilities, opt RenderOptions) ([]adapter.Artifact, error)
}

// ErrNotApplicable is returned by a Form's Render to say that this form does not fit the
// resource or the tool, and the next form should be tried. It is not a failure.
var ErrNotApplicable = errors.New("kind: this form does not apply to the resource")

// Needs lists the capabilities a form depends on. A form is considered for a tool when every
// field set here is supported by the tool; the zero value is satisfied by any tool.
type Needs struct {
	Skills   bool // the tool takes skill directories
	Commands bool // the tool takes slash commands
	Agents   bool // the tool takes sub-agents
	// MCP means the tool takes MCP servers over at least one transport. Whether it takes the
	// transport of a particular server is for Render to decide.
	MCP bool
}

// SatisfiedBy reports whether a tool with the given capabilities supports everything n asks for.
func (n Needs) SatisfiedBy(caps adapter.Capabilities) bool {
	return (!n.Skills || caps.Skills) &&
		(!n.Commands || caps.Commands) &&
		(!n.Agents || caps.Agents) &&
		(!n.MCP || caps.MCP.Stdio || caps.MCP.HTTP)
}

// RenderOptions is what the core tells a Render function besides the resource and the tool.
type RenderOptions struct {
	// Prefix is put in front of the resource's code to name every artifact. Everything the
	// product writes into a tool carries it, which is how a person, and the product, can tell
	// its entries from their own. A kind never spells the prefix out.
	Prefix string
}
