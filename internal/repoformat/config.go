// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package repoformat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/schema"
)

// FormatVersion is the major version of the repository format this package reads.
const FormatVersion = 1

// Config is rigdepot.yaml, the configuration at the root of a resource repository.
type Config struct {
	// Schema is the major version of the format the repository is written in.
	Schema int `json:"schema"`
	// Name is the display name of the repository; may be empty.
	Name string `json:"name,omitempty"`
	// Locale is the language of the fields without a translation. "en" when not set.
	Locale string `json:"locale,omitempty"`
	// Requires holds the minimum versions the repository needs.
	Requires Requires `json:"requires,omitzero"`
	// Stages are the delivery stages in display order: the eight default ones when the file
	// does not list any.
	Stages []resource.Stage `json:"stages,omitempty"`
	// Defaults apply to a resource that does not set the field itself.
	Defaults Defaults `json:"defaults,omitzero"`
	// Products lists the product codes the repository uses as scopes.
	Products []Product `json:"products,omitempty"`
	// Secrets defines the personal secrets resources may refer to, by name. Values are never in
	// a repository.
	Secrets map[string]Secret `json:"secrets,omitempty"`
}

// Requires holds the minimum versions a repository needs.
type Requires struct {
	// RGD is the lowest version of the command-line program that can read the repository,
	// written ">=1.2.3".
	RGD string `json:"rgd,omitempty"`
}

// Defaults are the values a resource gets for the fields it does not set.
type Defaults struct {
	Scope    resource.Scope    `json:"scope,omitempty"`
	Policy   resource.Policy   `json:"policy,omitempty"`
	Audience resource.Audience `json:"audience,omitempty"`
}

// Product is one product code a repository uses.
type Product struct {
	Code string `json:"code"`
	Name string `json:"name,omitempty"`
}

// Secret is the definition of a personal secret: what it is and where a person gets one.
type Secret struct {
	Label string `json:"label,omitempty"`
	Howto string `json:"howto,omitempty"`
}

// Stage returns the stage with the given identifier.
func (c Config) Stage(id string) (resource.Stage, bool) {
	for _, s := range c.Stages {
		if s.ID == id {
			return s, true
		}
	}
	return resource.Stage{}, false
}

// Product returns the product with the given code.
func (c Config) Product(code string) (Product, bool) {
	for _, p := range c.Products {
		if p.Code == code {
			return p, true
		}
	}
	return Product{}, false
}

// Effective returns m with the repository's defaults filled in for scope, policy and audience.
// A field that neither the resource nor the defaults set stays empty; what that means is up to
// whoever delivers the resource.
func (c Config) Effective(m resource.Meta) resource.Meta {
	if m.Scope == "" {
		m.Scope = c.Defaults.Scope
	}
	if m.Policy == "" {
		m.Policy = c.Defaults.Policy
	}
	if m.Audience == "" {
		m.Audience = c.Defaults.Audience
	}
	return m
}

// Issue is one thing wrong with a file in the repository.
type Issue struct {
	// File is the path of the file in the repository, with forward slashes. For a problem
	// with a resource's files as a whole it is the resource's path.
	File string
	// Line is where the problem is, from 1; 0 when it is not on one line.
	Line int
	// Field is the field the problem is about, written with dots ("meta.stages.1"); may be
	// empty.
	Field string
	// Code says what kind of problem it is; see the Code constants. It is stable and will be
	// the key of the message people read.
	Code string
	// Message describes the problem in English, for logs and tests.
	Message string
}

func (i Issue) String() string {
	loc := i.File
	if i.Line > 0 {
		loc += ":" + strconv.Itoa(i.Line)
	}
	if i.Field != "" {
		loc += " (" + i.Field + ")"
	}
	return loc + ": " + i.Code + ": " + i.Message
}

// The kinds of Issue.
const (
	CodeYAML            = "yaml"             // not well-formed YAML, or not in the subset used here
	CodeSchema          = "schema"           // does not match the JSON Schema
	CodeUnknownField    = "unknown-field"    // a field the format does not have (advisory)
	CodeUnknownDir      = "unknown-dir"      // a top-level directory no kind owns (advisory)
	CodeIgnored         = "ignored"          // an entry in a kind's directory that is not a resource (advisory)
	CodeUnknownProduct  = "unknown-product"  // a scope that is not among the products (advisory)
	CodeInvalidCode     = "invalid-code"     // the directory or file name is not a valid resource code
	CodeDuplicateCode   = "duplicate-code"   // two resources share a code
	CodeDuplicate       = "duplicate"        // a stage or product is listed twice
	CodeMissingMeta     = "missing-meta"     // no meta.yaml, or no "meta" key
	CodeUndefinedStage  = "undefined-stage"  // the resource names a stage the repository does not have
	CodeUndefinedSecret = "undefined-secret" // the resource names a secret rigdepot.yaml does not define
	CodeUndefinedRef    = "undefined-ref"    // the resource refers to a resource the repository does not have, or one that cannot be used
	CodeUnsafePath      = "unsafe-path"      // breaks a path safety rule; Field holds the rule
	CodeParse           = "parse"            // the kind refused the resource
	CodeRead            = "read"             // the files could not be read
)

// ErrNoConfig means the directory has no rigdepot.yaml, so it is not a resource repository.
var ErrNoConfig = errors.New("repoformat: no " + brand.RepoConfig + ": not a resource repository")

// ErrNewerFormat means the repository is written in a newer major version of the format. It is
// not read at all: guessing would deliver something other than what the authors meant.
var ErrNewerFormat = errors.New("repoformat: the repository uses a newer format")

// ErrNeedsNewer means the repository declares that it needs a newer program.
var ErrNeedsNewer = errors.New("repoformat: the repository needs a newer version")

// NewerFormatError says which version the repository has. It matches ErrNewerFormat.
type NewerFormatError struct{ Schema int }

func (e *NewerFormatError) Error() string {
	return fmt.Sprintf("repoformat: %s says schema %d, and this program reads up to %d; upgrade it", brand.RepoConfig, e.Schema, FormatVersion)
}

func (e *NewerFormatError) Is(target error) bool { return target == ErrNewerFormat }

// NeedsNewerError says which version the repository needs. It matches ErrNeedsNewer.
type NeedsNewerError struct {
	Requires string // as written, ">=0.4.0"
	Have     string
}

func (e *NeedsNewerError) Error() string {
	return fmt.Sprintf("repoformat: the repository requires %s %s, and this is %s; upgrade it", brand.CLI, e.Requires, e.Have)
}

func (e *NeedsNewerError) Is(target error) bool { return target == ErrNeedsNewer }

// ConfigError means rigdepot.yaml cannot be used. Nothing in the repository is read.
type ConfigError struct{ Issues []Issue }

func (e *ConfigError) Error() string {
	parts := make([]string, len(e.Issues))
	for i, is := range e.Issues {
		parts[i] = is.String()
	}
	return "repoformat: " + strings.Join(parts, "; ")
}

// readConfig reads and checks rigdepot.yaml. notices are advisory.
func readConfig(fsys fs.FS, version string) (cfg Config, notices []Issue, err error) {
	const file = brand.RepoConfig
	data, err := fs.ReadFile(fsys, file)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil, ErrNoConfig
	}
	if err != nil {
		return cfg, nil, fmt.Errorf("repoformat: reading %s: %w", file, err)
	}
	doc, err := parseYAML(data)
	if err != nil {
		return cfg, nil, &ConfigError{Issues: []Issue{yamlIssue(file, err)}}
	}
	// A newer major version is refused before validating: it need not match this schema.
	if m, ok := doc.value.(map[string]any); ok {
		if n, ok := m["schema"].(int); ok && n > FormatVersion {
			return cfg, nil, &NewerFormatError{Schema: n}
		}
	}
	schemas, err := embedded()
	if err != nil {
		return cfg, nil, err
	}
	if issues := validate(schemas[schema.Repo], doc, file, ""); len(issues) > 0 {
		return cfg, nil, &ConfigError{Issues: issues}
	}
	notices = unknownFields(schemas[schema.Repo], doc, file, "")
	if err := decode(doc.value, &cfg); err != nil {
		return cfg, nil, &ConfigError{Issues: []Issue{{File: file, Code: CodeSchema, Message: err.Error()}}}
	}
	var issues []Issue
	seen := map[string]bool{}
	for i, s := range cfg.Stages {
		if seen[s.ID] {
			ptr := fmt.Sprintf("/stages/%d/id", i)
			issues = append(issues, Issue{File: file, Line: doc.line(ptr), Field: field(ptr), Code: CodeDuplicate, Message: fmt.Sprintf("the stage %q is listed twice", s.ID)})
		}
		seen[s.ID] = true
	}
	seen = map[string]bool{}
	for i, p := range cfg.Products {
		if seen[p.Code] {
			ptr := fmt.Sprintf("/products/%d/code", i)
			issues = append(issues, Issue{File: file, Line: doc.line(ptr), Field: field(ptr), Code: CodeDuplicate, Message: fmt.Sprintf("the product %q is listed twice", p.Code)})
		}
		seen[p.Code] = true
	}
	if len(issues) > 0 {
		return cfg, nil, &ConfigError{Issues: issues}
	}
	if code, ok := cfg.Defaults.Scope.Product(); ok {
		if _, known := cfg.Product(code); !known {
			notices = append(notices, Issue{File: file, Line: doc.line("/defaults/scope"), Field: "defaults.scope", Code: CodeUnknownProduct,
				Message: fmt.Sprintf("the default scope %q is not among the products", code)})
		}
	}
	if len(cfg.Stages) == 0 {
		cfg.Stages = resource.DefaultStages()
	}
	if cfg.Locale == "" {
		cfg.Locale = "en"
	}
	if need, ok := strings.CutPrefix(cfg.Requires.RGD, ">="); ok {
		if older(version, strings.TrimSpace(need)) {
			return cfg, nil, &NeedsNewerError{Requires: cfg.Requires.RGD, Have: version}
		}
	}
	return cfg, notices, nil
}

// decode moves a value of the JSON data model into a Go value through its JSON names.
func decode(v any, out any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func yamlIssue(file string, err error) Issue {
	is := Issue{File: file, Code: CodeYAML, Message: err.Error()}
	var ye *yamlError
	if errors.As(err, &ye) {
		is.Line = ye.line
	}
	return is
}

// older reports whether version is a release older than need. A development build, or a
// version that cannot be read, is never older: it may be ahead of every release.
func older(version, need string) bool {
	have, ok := parseVersion(version)
	if !ok {
		return false
	}
	want, ok := parseVersion(need)
	if !ok {
		return false
	}
	for i := range have {
		if have[i] != want[i] {
			return have[i] < want[i]
		}
	}
	return false
}

// parseVersion reads "1.2.3", with or without a leading "v". A pre-release or build suffix
// makes it unreadable, which is what development builds ("0.0.0-dev+…") are.
func parseVersion(s string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
