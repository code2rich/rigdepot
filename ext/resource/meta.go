// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package resource

// Scope says who a resource is for: the whole organisation, one product, or only its author.
// Any value other than ScopeOrg and ScopeMe is the code of a product.
type Scope string

const (
	// ScopeOrg makes a resource available to everyone in the organisation.
	ScopeOrg Scope = "org"
	// ScopeMe marks a personal resource that only its author gets.
	ScopeMe Scope = "me"
)

// ProductScope returns the scope of the product with the given code.
func ProductScope(code string) Scope {
	return Scope(code)
}

// Product returns the product code when the scope is a product, and false for ScopeOrg, ScopeMe
// and the empty scope.
func (s Scope) Product() (code string, ok bool) {
	if s == "" || s == ScopeOrg || s == ScopeMe {
		return "", false
	}
	return string(s), true
}

// Policy says how a resource reaches the people in its scope. Whatever the policy, every write
// shows up in the plan first and a change to a tool's global configuration needs the person's
// consent.
type Policy string

const (
	// PolicyRequired installs the resource for everyone in scope; a person cannot turn it off.
	PolicyRequired Policy = "required"
	// PolicyRecommended installs the resource by default; a person can turn it off.
	PolicyRecommended Policy = "recommended"
	// PolicyOptional installs the resource only for people who turn it on.
	PolicyOptional Policy = "optional"
)

// Valid reports whether p is one of the three policies.
func (p Policy) Valid() bool {
	return p == PolicyRequired || p == PolicyRecommended || p == PolicyOptional
}

// Audience says which kind of work a resource supports.
type Audience string

const (
	// AudienceRD is for engineering work: writing, testing and shipping software.
	AudienceRD Audience = "rd"
	// AudienceMgmt is for collaboration work around it: planning, reporting, coordination.
	AudienceMgmt Audience = "mgmt"
)

// Valid reports whether a is one of the two audiences.
func (a Audience) Valid() bool {
	return a == AudienceRD || a == AudienceMgmt
}

// Stage is one stage of the delivery process, such as "develop" or "test". Resources name the
// stages they belong to, and the product groups, recommends and notifies by stage.
type Stage struct {
	// ID is the stable identifier resources refer to. A stage that resources have referred to
	// is never removed, only hidden.
	ID string `json:"id"`
	// Name is the display name a repository gives the stage. Empty for the default stages,
	// whose names come from the product's translations.
	Name string `json:"name,omitempty"`
	// Hidden keeps a stage out of the user interface while resources that name it stay valid.
	Hidden bool `json:"hidden,omitempty"`
}

// DefaultStages returns the eight stages a repository has when its rigdepot.yaml does not list
// any, in order. The slice is the caller's own.
func DefaultStages() []Stage {
	return []Stage{
		{ID: "requirement"},
		{ID: "architecture"},
		{ID: "design"},
		{ID: "develop"},
		{ID: "test"},
		{ID: "scm"},
		{ID: "release"},
		{ID: "ops"},
	}
}

// Meta is the metadata every resource carries, whatever its kind: what it is called, who it is
// for and when to use it. Directory-shaped kinds keep it in meta.yaml; single-file kinds keep it
// under the "meta" key of their file. The field names in those files are the JSON names below.
//
// Scope and Policy in a repository are suggestions: once an administrator changes either on the
// server, the server's value wins and later changes in the repository are only reported.
type Meta struct {
	// Name is the display name. It is the only required field.
	Name string `json:"name"`
	// Summary is one sentence for people. A skill without one shows the description from its
	// SKILL.md instead.
	Summary string `json:"summary,omitempty"`
	// Stages lists the identifiers of the stages the resource belongs to. It may be empty.
	Stages []string `json:"stages,omitempty"`
	// Audience, Scope and Policy are empty when the resource does not say; the repository's
	// defaults then apply.
	Audience Audience `json:"audience,omitempty"`
	Scope    Scope    `json:"scope,omitempty"`
	Policy   Policy   `json:"policy,omitempty"`
	// When describes the situation the resource is for. Recommendations match a task against
	// it on the person's own computer.
	When string `json:"when,omitempty"`
	// Try is a sentence a person can send to their AI tool as it is to see the resource work.
	Try string `json:"try,omitempty"`
	// Secrets names the personal secrets the resource needs. Secrets an MCP server refers to
	// in its env or headers count without being listed here.
	Secrets []string `json:"secrets,omitempty"`
	// Maintainers lists account names on the server.
	Maintainers []string `json:"maintainers,omitempty"`
	// Tags are free-form labels.
	Tags []string `json:"tags,omitempty"`
	// I18n holds the fields people read in other languages, keyed by BCP 47 language tag. The
	// fields above are in the repository's own language (rigdepot.yaml "locale", default "en").
	//
	// Only what people read is translated. What an AI reads, such as SKILL.md, exists once.
	I18n map[string]Text `json:"i18n,omitempty"`
}

// Text is the part of Meta that people read, in one language. An empty field means there is no
// translation for it and the untranslated value is shown.
type Text struct {
	Name    string `json:"name,omitempty"`
	Summary string `json:"summary,omitempty"`
	When    string `json:"when,omitempty"`
	Try     string `json:"try,omitempty"`
}
