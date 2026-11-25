// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package check

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// bundledEnv is the example rule from the contributor guide: a resource must not ship a .env
// file. That it compiles against this package is part of the contract.
type bundledEnv struct{ id string }

func (r bundledEnv) ID() string          { return r.id }
func (bundledEnv) Severity() Severity    { return MustFix }
func (bundledEnv) Applies(k string) bool { return k == resource.KindSkill || k == resource.KindAgent }

func (r bundledEnv) Check(ctx context.Context, in Input) ([]Finding, error) {
	var found []Finding
	err := fs.WalkDir(in.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err // unreadable: the engine counts this as not passed, never as clean
		}
		if !d.IsDir() && path.Base(p) == ".env" {
			found = append(found, Finding{
				Rule:     r.id,
				Severity: MustFix,
				File:     p,
				Message:  "check.bundledenv.found",
				Params:   map[string]any{"file": p},
			})
		}
		return nil
	})
	return found, err
}

var _ Rule = bundledEnv{}

func TestRuleSeesOnlyTheResourceItIsGiven(t *testing.T) {
	rule := bundledEnv{id: "bundledenv"}
	ref := resource.Ref{Kind: resource.KindSkill, Code: "unit-testing"}

	hit := fstest.MapFS{
		"SKILL.md":         {Data: []byte("---\nname: unit-testing\n---\n")},
		"scripts/.env":     {Data: []byte("TOKEN=x\n")},
		"scripts/setup.sh": {Data: []byte("#!/bin/sh\n")},
	}
	found, err := rule.Check(context.Background(), Input{Ref: ref, Meta: resource.Meta{Name: "Unit testing"}, Files: hit})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(found), found)
	}
	f := found[0]
	if f.Rule != "bundledenv" || f.Severity != MustFix || f.File != "scripts/.env" || f.Line != 0 || f.Message != "check.bundledenv.found" {
		t.Errorf("finding = %+v", f)
	}

	// Looks similar, must not be hit.
	miss := fstest.MapFS{
		"SKILL.md":     {Data: []byte("---\nname: unit-testing\n---\n")},
		".env.example": {Data: []byte("TOKEN=\n")},
	}
	if found, err := rule.Check(context.Background(), Input{Ref: ref, Files: miss}); err != nil || len(found) != 0 {
		t.Errorf("a resource with only .env.example: findings %+v, error %v", found, err)
	}
}

// unreadable is a file system on which nothing can be read.
type unreadable struct{}

func (unreadable) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
}

// A rule that cannot read the resource says so. "Could not look" must never come back as "looked
// and found nothing".
func TestUnreadableResourceIsAnErrorNotAPass(t *testing.T) {
	found, err := bundledEnv{id: "bundledenv"}.Check(context.Background(), Input{Files: unreadable{}})
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("Check on an unreadable resource: findings %+v, error %v; want the read error", found, err)
	}
}

func TestSeverity(t *testing.T) {
	var zero Severity
	if zero != Advisory {
		t.Errorf("the zero Severity is %v, want Advisory", zero)
	}
	if Advisory.String() != "advisory" || MustFix.String() != "must_fix" {
		t.Errorf("Severity names are %q and %q; they appear in machine-readable output and must not change", Advisory, MustFix)
	}
}

// A finding is stored and sent as JSON; its level is written by name and read back only by name.
func TestSeverityInJSON(t *testing.T) {
	data, err := json.Marshal(Finding{Rule: "secrets", Severity: MustFix, Message: "check.secrets.token_like"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"Severity":"must_fix"`) {
		t.Errorf("a must-fix finding is written as %s", data)
	}
	for name, want := range map[string]Severity{`"advisory"`: Advisory, `"must_fix"`: MustFix} {
		var got Severity
		if err := json.Unmarshal([]byte(name), &got); err != nil || got != want {
			t.Errorf("reading %s: %v, %v; want %v", name, got, err, want)
		}
	}
	for _, bad := range []string{`"should_fix"`, `"MustFix"`, `""`, `1`} {
		got := MustFix
		if err := json.Unmarshal([]byte(bad), &got); err == nil {
			t.Errorf("reading %s gave %v and no error", bad, got)
		}
	}
	if _, err := json.Marshal(Severity(7)); err == nil {
		t.Error("an undefined severity was written without an error")
	}
}

func ruleIDs(rules []Rule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.ID())
	}
	return out
}

// mustPanic runs fn and returns what it panicked with. The test fails if fn returns normally.
func mustPanic(t *testing.T, what string, fn func()) (msg string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("%s did not panic", what)
		}
		msg, _ = r.(string)
	}()
	fn()
	return ""
}

func TestRegistryReturnsRulesSortedByID(t *testing.T) {
	g := NewRegistry()
	for _, id := range []string{"structure", "secrets", "dangerous", "bundledenv"} {
		g.Register(bundledEnv{id: id})
	}
	if got, want := ruleIDs(g.All()), []string{"bundledenv", "dangerous", "secrets", "structure"}; !slices.Equal(got, want) {
		t.Errorf("All() = %v, want %v", got, want)
	}
}

func TestRegistryPanicsOnDuplicateID(t *testing.T) {
	g := NewRegistry()
	g.Register(bundledEnv{id: "secrets"})
	msg := mustPanic(t, "registering the ID secrets twice", func() { g.Register(bundledEnv{id: "secrets"}) })
	if !strings.Contains(msg, "check rule") || !strings.Contains(msg, `"secrets"`) {
		t.Errorf("panic message %q does not say which rule is registered twice", msg)
	}
}

func TestRegistryFreezesOnFirstRead(t *testing.T) {
	reads := map[string]func(*Registry){
		"All":     func(g *Registry) { g.All() },
		"Lookup":  func(g *Registry) { g.Lookup("secrets") },
		"Ruleset": func(g *Registry) { g.Ruleset() },
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			g := NewRegistry()
			g.Register(bundledEnv{id: "secrets"})
			read(g)
			mustPanic(t, "registering after "+name, func() { g.Register(bundledEnv{id: "structure"}) })
			msg := mustPanic(t, "setting the version after "+name, func() { g.SetVersion(2) })
			if !strings.Contains(msg, "version") {
				t.Errorf("panic message %q does not say that it was the version that came too late", msg)
			}
			rs := g.Ruleset()
			if !slices.Equal(ruleIDs(rs.Rules), []string{"secrets"}) || rs.Version != 0 {
				t.Errorf("the late changes got in anyway: version %d, rules %v", rs.Version, ruleIDs(rs.Rules))
			}
		})
	}
}

func TestRegistryPanicsOnNilAndMalformedID(t *testing.T) {
	g := NewRegistry()
	mustPanic(t, "Register(nil)", func() { g.Register(nil) })
	for _, id := range []string{"", "Secrets", "token_like"} {
		msg := mustPanic(t, "registering the ID "+id, func() { g.Register(bundledEnv{id: id}) })
		if !strings.Contains(msg, "malformed") {
			t.Errorf("Register with ID %q: panic message %q", id, msg)
		}
	}
	if got := g.All(); len(got) != 0 {
		t.Errorf("a refused rule is in the registry: %v", ruleIDs(got))
	}
}

func TestRulesetCarriesTheDeclaredVersion(t *testing.T) {
	g := NewRegistry()
	g.Register(bundledEnv{id: "structure"})
	g.SetVersion(3)
	g.Register(bundledEnv{id: "secrets"}) // the order of the two calls does not matter
	rs := g.Ruleset()
	if rs.Version != 3 {
		t.Errorf("Ruleset().Version = %d, want 3", rs.Version)
	}
	if got, want := ruleIDs(rs.Rules), []string{"secrets", "structure"}; !slices.Equal(got, want) {
		t.Errorf("Ruleset().Rules = %v, want %v", got, want)
	}
	if r, ok := g.Lookup("secrets"); !ok || r.ID() != "secrets" {
		t.Errorf(`Lookup("secrets") = %v, %v`, r, ok)
	}
}

func TestRulesetWithoutADeclaredVersionSaysSo(t *testing.T) {
	g := NewRegistry()
	g.Register(bundledEnv{id: "structure"})
	if v := g.Ruleset().Version; v != 0 {
		t.Errorf("nobody declared a version, but Ruleset().Version = %d", v)
	}
}

func TestSetVersionRefusesBadVersions(t *testing.T) {
	for _, v := range []int{0, -1} {
		g := NewRegistry()
		mustPanic(t, "SetVersion with a version below 1", func() { g.SetVersion(v) })
		if got := g.Ruleset().Version; got != 0 {
			t.Errorf("SetVersion(%d) was refused but the version is %d", v, got)
		}
	}
	g := NewRegistry()
	g.SetVersion(1)
	msg := mustPanic(t, "declaring the version twice", func() { g.SetVersion(2) })
	if !strings.Contains(msg, "twice") {
		t.Errorf("panic message %q does not say the version was declared twice", msg)
	}
	if got := g.Ruleset().Version; got != 1 {
		t.Errorf("after a refused second declaration the version is %d, want 1", got)
	}
}

// The package-level functions are the registry of the running program. This is the only test
// that touches it; nothing registers rules in this test binary.
func TestDefaultRegistry(t *testing.T) {
	Register(bundledEnv{id: "structure"})
	SetVersion(1)
	Register(bundledEnv{id: "bundledenv"})
	rs := DefaultRuleset()
	if rs.Version != 1 || !slices.Equal(ruleIDs(rs.Rules), []string{"bundledenv", "structure"}) {
		t.Errorf("DefaultRuleset() = version %d, rules %v", rs.Version, ruleIDs(rs.Rules))
	}
	if got := ruleIDs(All()); !slices.Equal(got, []string{"bundledenv", "structure"}) {
		t.Errorf("All() = %v", got)
	}
	if r, ok := Lookup("structure"); !ok || r.ID() != "structure" {
		t.Errorf(`Lookup("structure") = %v, %v`, r, ok)
	}
	mustPanic(t, "Register after the default registry was read", func() { Register(bundledEnv{id: "secrets"}) })
	mustPanic(t, "SetVersion after the default registry was read", func() { SetVersion(2) })
}
