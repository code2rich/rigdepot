// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package check

import (
	"context"
	"io/fs"

	"github.com/code2rich/rigdepot/ext/resource"
)

// Severity says what a finding means for publishing. The zero value is Advisory.
type Severity int

const (
	// Advisory findings are shown to the author and block nothing.
	Advisory Severity = iota
	// MustFix findings block publishing. A personal resource with one cannot be used even by
	// its author, so that nothing with a credential in it gets installed anywhere.
	MustFix
)

// String returns "advisory" or "must_fix", the names used in machine-readable output.
func (s Severity) String() string {
	if s == MustFix {
		return "must_fix"
	}
	return "advisory"
}

// Rule is one check. An implementation is stateless and safe to call from several goroutines.
type Rule interface {
	// ID returns the stable identifier of the rule, in lower case with hyphens, such as
	// "secrets". It is recorded in check results and in the audit log and is part of the
	// rule's message keys ("check.secrets.…"), so it cannot change after a release.
	ID() string

	// Severity returns the level of the rule's findings.
	Severity() Severity

	// Applies reports whether the rule checks resources of the given kind (resource.Ref.Kind).
	// The engine does not call Check for other kinds.
	Applies(kind string) bool

	// Check returns what the rule found in one resource, in a stable order, or nothing.
	//
	// A non-nil error means the rule could not finish, for example because a file could not
	// be read. The engine then counts the resource as not having passed this rule. It is never
	// treated as "nothing found".
	Check(ctx context.Context, in Input) ([]Finding, error)
}

// Input is everything a rule may look at.
type Input struct {
	// Ref identifies the resource being checked.
	Ref resource.Ref
	// Meta is the resource's metadata, already parsed.
	Meta resource.Meta
	// Files holds every file of the resource, read-only, laid out as resource.Bundle.Files
	// describes: the resource's directory, or a directory holding the resource's one file.
	Files fs.FS
	// Peers describes the other published resources, for rules that compare. It is empty when
	// a resource is checked on its own, as rgd check does without a server; a rule that needs
	// peers then has nothing to report.
	Peers Peers
}

// Peers describes the resources that are already published where the checked resource would be
// published. The resource being checked is not among them, even when an earlier version of it is
// published.
type Peers []Peer

// Peer is what a rule can know about another resource: enough to compare, never its files.
type Peer struct {
	Ref  resource.Ref
	Meta resource.Meta
	// Description is the text an AI tool reads to decide whether to use the resource: for a
	// skill, the description in the front matter of its SKILL.md.
	Description string
}

// Finding is one problem a rule found.
type Finding struct {
	// Rule is the ID of the rule that reports it.
	Rule string
	// Severity is the level of this finding, normally the rule's own.
	Severity Severity
	// File is the path of the file concerned, relative to Input.Files, with forward slashes.
	// Empty when the finding is not about one file.
	File string
	// Line is the line in File, counted from 1. Zero when the finding is not about one line.
	Line int
	// Message is the key of the text shown to people, such as "check.secrets.token_like". The
	// text itself lives in the message catalogues, once per language.
	Message string
	// Params are the values filled into the text. They must not contain what a rule about
	// sensitive content matched: give the type of the thing and at most a masked fragment.
	Params map[string]any
}

// Ruleset is a list of rules with a version number.
//
// The version is recorded with every check result. When a rule set is upgraded, published
// resources are checked again, and for one period the engine shows problems the new version
// brought as advisory before they start to block; rules do not have to care. Whoever puts a rule
// set together owns its version and raises it whenever a rule is added, removed or changed in
// what it reports.
type Ruleset struct {
	// Version starts at 1. Zero means nobody declared one.
	Version int
	// Rules is sorted by ID.
	Rules []Rule
}
