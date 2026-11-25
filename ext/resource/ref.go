// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package resource

import (
	"regexp"
	"strconv"
)

// The identifiers of the resource kinds in repository format v1. The set of kinds is open: a
// kind is whatever is registered with package kind, and these constants only save built-in code
// and check rules from spelling the common ones out.
const (
	KindSkill     = "skill"     // skills/<code>/, a directory with a SKILL.md
	KindMCP       = "mcp"       // mcp/<code>.yaml, one MCP server
	KindWorkflow  = "workflow"  // workflows/<code>.yaml, ordered steps with checks
	KindAgent     = "agent"     // agents/<code>/, a role with its own skills
	KindKnowledge = "knowledge" // knowledge/<code>.yaml, a pointer to a knowledge base
)

// Ref identifies one resource, and optionally one published version of it. It is comparable, so
// it can be a map key.
type Ref struct {
	// Kind is the identifier of the resource kind, such as "skill". See package kind.
	Kind string

	// Code is the resource's identifier: the directory name, or the file name without its
	// extension, in the resource repository. It is unique in a repository across all kinds and
	// never changes once the resource is published. See ValidCode for its shape.
	//
	// The name a resource gets inside a tool is the product's prefix followed by Code.
	Code string

	// Version is the number the server assigned when this content was published, starting at 1.
	// Zero means there is no published version: the resource comes straight from a repository
	// (rgd sync --repo, rgd check), where its content digest tells one state from another.
	Version int
}

// String returns the reference in the form "skill/unit-testing@5", or "skill/unit-testing" when
// there is no version. It is meant for logs and error messages.
func (r Ref) String() string {
	s := r.Kind + "/" + r.Code
	if r.Version > 0 {
		s += "@" + strconv.Itoa(r.Version)
	}
	return s
}

// The shape of a resource code. The upper bound leaves room for the prefix: the name inside a
// tool is prefix plus code, and the tools recommend at most 64 characters for a skill name.
const (
	// CodePattern is the regular expression a resource code matches: lower-case letters and
	// digits in groups joined by single hyphens.
	CodePattern = `^[a-z0-9]+(-[a-z0-9]+)*$`
	// MinCodeLength is the shortest a resource code may be.
	MinCodeLength = 2
	// MaxCodeLength is the longest a resource code may be.
	MaxCodeLength = 48
)

var codePattern = regexp.MustCompile(CodePattern)

// ValidCode reports whether code is a well-formed resource code: it matches CodePattern and is
// MinCodeLength to MaxCodeLength characters long.
func ValidCode(code string) bool {
	return len(code) >= MinCodeLength && len(code) <= MaxCodeLength && codePattern.MatchString(code)
}

// SecretNamePattern is the regular expression the name of a personal secret matches: an
// upper-case letter followed by 1 to 63 upper-case letters, digits and underscores, the way
// environment variables are named.
//
// A repository defines its secrets by name in rigdepot.yaml and resources refer to them by that
// name. The value of a secret is never part of a repository, a bundle or any value in ext/.
const SecretNamePattern = `^[A-Z][A-Z0-9_]{1,63}$`

var secretNamePattern = regexp.MustCompile(SecretNamePattern)

// ValidSecretName reports whether name is a well-formed secret name.
func ValidSecretName(name string) bool {
	return secretNamePattern.MatchString(name)
}
