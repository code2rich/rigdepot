// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package check

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/resource"
)

// Message keys of the findings the engine itself reports.
const (
	// MsgRuleFailed is the key of the finding that stands in for a rule that could not finish.
	// Its parameters are "rule" and "error".
	MsgRuleFailed = "check.engine.rule_failed"
)

// Resource is one resource as the engine receives it: everything is already in memory.
type Resource struct {
	// Ref identifies the resource: its kind and code, and its version when it has one.
	Ref resource.Ref
	// Meta is the resource's metadata, already parsed.
	Meta resource.Meta
	// Files maps the path of each file, relative to the resource and with forward slashes, to
	// its content, laid out as resource.Bundle.Files describes. Directories are implied by the
	// paths; there are no entries for them.
	Files map[string][]byte
}

// Result is what one rule set found in one resource.
type Result struct {
	// Ref is the resource that was checked.
	Ref resource.Ref
	// RulesetVersion is the version of the rule set the result comes from.
	RulesetVersion int
	// Rules lists the IDs of the rules that applied to the resource's kind and ran, in order.
	Rules []string
	// Findings are sorted by file, line, rule and message.
	Findings []extcheck.Finding
}

// MustFix returns the number of findings that block publishing.
func (r Result) MustFix() int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == extcheck.MustFix {
			n++
		}
	}
	return n
}

// Passed reports whether nothing in the result blocks publishing.
func (r Result) Passed() bool { return r.MustFix() == 0 }

// Engine runs one rule set. It is safe for use by several goroutines.
type Engine struct {
	rules extcheck.Ruleset
}

// New returns an engine for the rule set. It refuses a rule set without a version, because a
// result that cannot say which rules produced it cannot be compared with a later one.
func New(rs extcheck.Ruleset) (*Engine, error) {
	if rs.Version < 1 {
		return nil, fmt.Errorf("check: the rule set has no version (got %d); results must say which rules produced them", rs.Version)
	}
	seen := map[string]bool{}
	for _, r := range rs.Rules {
		if r == nil {
			return nil, errors.New("check: the rule set contains a nil rule")
		}
		if seen[r.ID()] {
			return nil, fmt.Errorf("check: the rule set contains the rule %q twice", r.ID())
		}
		seen[r.ID()] = true
	}
	return &Engine{rules: rs}, nil
}

// Version returns the version of the engine's rule set.
func (e *Engine) Version() int { return e.rules.Version }

// Check runs every rule that applies to the resource's kind. Peers are the other resources the
// rules may compare against; they may be empty.
//
// The error is not about the resource: it is returned when the resource is malformed as an
// argument (a path that is not a valid, slash-separated relative path) or when ctx ends before
// every rule ran. The result is then incomplete and must not be used as an answer.
func (e *Engine) Check(ctx context.Context, res Resource, peers extcheck.Peers) (Result, error) {
	in, err := NewInput(res, peers)
	if err != nil {
		return Result{}, err
	}
	out := Result{Ref: res.Ref, RulesetVersion: e.rules.Version, Findings: []extcheck.Finding{}}
	for _, r := range e.rules.Rules {
		if err := ctx.Err(); err != nil {
			return Result{}, fmt.Errorf("check %s: %w", res.Ref, err)
		}
		if !r.Applies(res.Ref.Kind) {
			continue
		}
		out.Rules = append(out.Rules, r.ID())
		found, err := runRule(ctx, r, in)
		if err == nil {
			err = validate(r, res.Files, found)
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Result{}, fmt.Errorf("check %s: %w", res.Ref, ctxErr)
			}
			found = []extcheck.Finding{failed(r, err)}
		}
		for _, f := range found {
			f.Rule = r.ID()
			out.Findings = append(out.Findings, f)
		}
	}
	sortFindings(out.Findings)
	return out, nil
}

// sortFindings puts findings in the order results have: by file, line, rule and message.
func sortFindings(found []extcheck.Finding) {
	slices.SortStableFunc(found, func(a, b extcheck.Finding) int {
		return cmp.Or(
			cmp.Compare(a.File, b.File),
			cmp.Compare(a.Line, b.Line),
			cmp.Compare(a.Rule, b.Rule),
			cmp.Compare(a.Message, b.Message),
		)
	})
}

// CheckAll checks every resource, each against all the others as its peers: resources that are
// checked together are published together, so two of them that an AI tool cannot tell apart are a
// problem. The results are in the order of the resources.
func (e *Engine) CheckAll(ctx context.Context, all []Resource) ([]Result, error) {
	out := make([]Result, 0, len(all))
	for _, res := range all {
		r, err := e.Check(ctx, res, PeersOf(all, res.Ref))
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// runRule calls the rule and turns a panic into an error, so that one broken rule cannot take
// the whole check down or make it look as if the resource had passed.
func runRule(ctx context.Context, r extcheck.Rule, in extcheck.Input) (found []extcheck.Finding, err error) {
	defer func() {
		if p := recover(); p != nil {
			found, err = nil, fmt.Errorf("the rule panicked: %v", p)
		}
	}()
	return r.Check(ctx, in)
}

// failed is the finding that stands in for a rule that could not finish. It has the rule's own
// severity: a must-fix rule that could not look blocks, as if it had found something.
func failed(r extcheck.Rule, err error) extcheck.Finding {
	return extcheck.Finding{
		Rule:     r.ID(),
		Severity: r.Severity(),
		Message:  MsgRuleFailed,
		Params:   map[string]any{"rule": r.ID(), "error": err.Error()},
	}
}

// validate makes sure that every finding points at something the resource has: a file that
// exists, a line inside it. A finding that does not is a bug in the rule, and the rule is then
// treated as not having finished.
func validate(r extcheck.Rule, files map[string][]byte, found []extcheck.Finding) error {
	prefix := "check." + r.ID() + "."
	for _, f := range found {
		switch {
		case f.Rule != "" && f.Rule != r.ID():
			return fmt.Errorf("a finding names the rule %q", f.Rule)
		case !strings.HasPrefix(f.Message, prefix) || len(f.Message) == len(prefix):
			return fmt.Errorf("the message key %q does not start with %q", f.Message, prefix)
		case f.Line < 0:
			return fmt.Errorf("a finding in %q has the line %d", f.File, f.Line)
		case f.File == "" && f.Line != 0:
			return fmt.Errorf("a finding has the line %d but no file", f.Line)
		}
		if f.File == "" {
			continue
		}
		data, ok := files[f.File]
		if !ok {
			return fmt.Errorf("a finding is about %q, which the resource does not have", f.File)
		}
		if f.Line > lineCount(data) {
			return fmt.Errorf("a finding is about line %d of %q, which has %d lines", f.Line, f.File, lineCount(data))
		}
	}
	return nil
}

// lineCount returns the number of lines in data: a last line without a newline counts.
func lineCount(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte("\n"))
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// NewInput returns what a rule receives for the resource: its files as an in-memory, read-only
// file system and nothing else. It fails when a path is not a valid relative path with forward
// slashes, or when a file's path is also used as a directory.
func NewInput(res Resource, peers extcheck.Peers) (extcheck.Input, error) {
	files, err := newMemFS(res.Files)
	if err != nil {
		return extcheck.Input{}, fmt.Errorf("check %s: %w", res.Ref, err)
	}
	return extcheck.Input{Ref: res.Ref, Meta: res.Meta, Files: files, Peers: peers}, nil
}

// PeersOf describes every resource in all except the one with the given code, as peers of it.
func PeersOf(all []Resource, self resource.Ref) extcheck.Peers {
	peers := extcheck.Peers{}
	for _, res := range all {
		if res.Ref.Code == self.Code {
			continue
		}
		peers = append(peers, extcheck.Peer{Ref: res.Ref, Meta: res.Meta, Description: Description(res)})
	}
	return peers
}

// Description returns the text an AI tool reads to decide whether to use the resource: for a
// skill, the description in the front matter of its SKILL.md; for other kinds, the summary in
// its metadata. It is empty when there is none or when the front matter cannot be read.
func Description(res Resource) string {
	if res.Ref.Kind == resource.KindSkill {
		d, _ := frontMatterDescription(res.Files["SKILL.md"])
		return d
	}
	return res.Meta.Summary
}
