// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package check

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// fakeRule reports whatever its function returns.
type fakeRule struct {
	id       string
	severity extcheck.Severity
	kinds    []string
	check    func(in extcheck.Input) ([]extcheck.Finding, error)
}

func (r fakeRule) ID() string                  { return r.id }
func (r fakeRule) Severity() extcheck.Severity { return r.severity }
func (r fakeRule) Applies(kind string) bool {
	return len(r.kinds) == 0 || slices.Contains(r.kinds, kind)
}
func (r fakeRule) Check(_ context.Context, in extcheck.Input) ([]extcheck.Finding, error) {
	return r.check(in)
}

func engine(t *testing.T, rules ...extcheck.Rule) *Engine {
	t.Helper()
	e, err := New(extcheck.Ruleset{Version: 7, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

var skill = Resource{
	Ref:  resource.Ref{Kind: resource.KindSkill, Code: "unit-testing"},
	Meta: resource.Meta{Name: "Unit testing"},
	Files: map[string][]byte{
		"SKILL.md":          []byte("---\nname: unit-testing\ndescription: Write unit tests when a change needs them\n---\n\nStep one.\n"),
		"meta.yaml":         []byte("name: Unit testing\n"),
		"scripts/run.sh":    []byte("#!/bin/sh\necho run\n"),
		"references/a/b.md": []byte("deep\n"),
	},
}

func TestNewRefusesARulesetWithoutAVersion(t *testing.T) {
	if _, err := New(extcheck.Ruleset{Rules: []extcheck.Rule{fakeRule{id: "a"}}}); err == nil {
		t.Error("New accepted a rule set with version 0")
	}
	if _, err := New(extcheck.Ruleset{Version: 1, Rules: []extcheck.Rule{fakeRule{id: "a"}, fakeRule{id: "a"}}}); err == nil {
		t.Error("New accepted a rule set with the same rule twice")
	}
	if _, err := New(extcheck.Ruleset{Version: 1, Rules: []extcheck.Rule{nil}}); err == nil {
		t.Error("New accepted a nil rule")
	}
}

func TestResultCarriesVersionFileAndLine(t *testing.T) {
	r := fakeRule{id: "lines", severity: extcheck.MustFix, check: func(extcheck.Input) ([]extcheck.Finding, error) {
		return []extcheck.Finding{
			{Severity: extcheck.MustFix, File: "scripts/run.sh", Line: 2, Message: "check.lines.b"},
			{Severity: extcheck.MustFix, File: "SKILL.md", Line: 3, Message: "check.lines.a"},
			{Severity: extcheck.Advisory, Message: "check.lines.whole"},
		}, nil
	}}
	got, err := engine(t, r).Check(context.Background(), skill, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.RulesetVersion != 7 || got.Ref != skill.Ref || !slices.Equal(got.Rules, []string{"lines"}) {
		t.Errorf("result header = version %d, ref %v, rules %v", got.RulesetVersion, got.Ref, got.Rules)
	}
	want := []extcheck.Finding{
		{Rule: "lines", Severity: extcheck.Advisory, Message: "check.lines.whole"},
		{Rule: "lines", Severity: extcheck.MustFix, File: "SKILL.md", Line: 3, Message: "check.lines.a"},
		{Rule: "lines", Severity: extcheck.MustFix, File: "scripts/run.sh", Line: 2, Message: "check.lines.b"},
	}
	if !reflect.DeepEqual(got.Findings, want) {
		t.Errorf("findings =\n%+v\nwant\n%+v", got.Findings, want)
	}
	if got.MustFix() != 2 || got.Passed() {
		t.Errorf("MustFix() = %d, Passed() = %v", got.MustFix(), got.Passed())
	}
}

func TestCleanResultPassesAndHasAnEmptyList(t *testing.T) {
	r := fakeRule{id: "quiet", check: func(extcheck.Input) ([]extcheck.Finding, error) { return nil, nil }}
	got, err := engine(t, r).Check(context.Background(), skill, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Passed() || got.Findings == nil || len(got.Findings) != 0 {
		t.Errorf("a clean result: passed %v, findings %#v", got.Passed(), got.Findings)
	}
}

// The guarantee that a rule cannot read the disk: what it gets is the resource in memory, and
// nothing on disk is reachable from it, not even a file that exists next to the test.
func TestRulesSeeOnlyTheResourceInMemory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "on-disk.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	var tried []string
	r := fakeRule{id: "snoop", check: func(in extcheck.Input) ([]extcheck.Finding, error) {
		for _, name := range []string{"on-disk.txt", "../on-disk.txt", filepath.Join(dir, "on-disk.txt"), "/etc/hosts", "engine.go"} {
			if _, err := fs.ReadFile(in.Files, name); err == nil {
				tried = append(tried, name)
			}
		}
		if err := fstest.TestFS(in.Files, "SKILL.md", "meta.yaml", "scripts/run.sh", "references/a/b.md"); err != nil {
			return nil, err
		}
		return nil, nil
	}}
	got, err := engine(t, r).Check(context.Background(), skill, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tried) > 0 {
		t.Errorf("a rule read files from the disk: %v", tried)
	}
	if len(got.Findings) != 0 {
		t.Errorf("the in-memory file system is not a valid fs.FS: %+v", got.Findings)
	}

	// Input has no field that gives a rule any other capability.
	typ := reflect.TypeFor[extcheck.Input]()
	var fields []string
	for i := range typ.NumField() {
		fields = append(fields, typ.Field(i).Name)
	}
	if !slices.Equal(fields, []string{"Ref", "Meta", "Files", "Peers"}) {
		t.Errorf("check.Input has the fields %v; anything new there is something every rule can reach", fields)
	}
}

func TestARuleCannotChangeWhatTheNextRuleReads(t *testing.T) {
	scribble := fakeRule{id: "a", check: func(in extcheck.Input) ([]extcheck.Finding, error) {
		data, err := fs.ReadFile(in.Files, "SKILL.md")
		if err != nil {
			return nil, err
		}
		for i := range data {
			data[i] = 'x'
		}
		return nil, nil
	}}
	var seen []byte
	read := fakeRule{id: "b", check: func(in extcheck.Input) ([]extcheck.Finding, error) {
		var err error
		seen, err = fs.ReadFile(in.Files, "SKILL.md")
		return nil, err
	}}
	if _, err := engine(t, scribble, read).Check(context.Background(), skill, nil); err != nil {
		t.Fatal(err)
	}
	if string(seen) != string(skill.Files["SKILL.md"]) {
		t.Errorf("the second rule read %q", seen)
	}
}

// A rule that could not finish has not passed. Each way of not finishing becomes one finding at
// the rule's own severity.
func TestARuleThatCannotFinishDoesNotPass(t *testing.T) {
	cases := map[string]func(extcheck.Input) ([]extcheck.Finding, error){
		"error": func(in extcheck.Input) ([]extcheck.Finding, error) {
			return []extcheck.Finding{{Message: "check.broken.x"}}, &fs.PathError{Op: "read", Path: "SKILL.md", Err: fs.ErrPermission}
		},
		"panic": func(extcheck.Input) ([]extcheck.Finding, error) { panic("index out of range") },
		"file it does not have": func(extcheck.Input) ([]extcheck.Finding, error) {
			return []extcheck.Finding{{File: "nope.md", Message: "check.broken.x"}}, nil
		},
		"line past the end": func(extcheck.Input) ([]extcheck.Finding, error) {
			return []extcheck.Finding{{File: "scripts/run.sh", Line: 3, Message: "check.broken.x"}}, nil
		},
		"line without a file": func(extcheck.Input) ([]extcheck.Finding, error) {
			return []extcheck.Finding{{Line: 1, Message: "check.broken.x"}}, nil
		},
		"negative line": func(extcheck.Input) ([]extcheck.Finding, error) {
			return []extcheck.Finding{{File: "SKILL.md", Line: -1, Message: "check.broken.x"}}, nil
		},
		"another rule's message": func(extcheck.Input) ([]extcheck.Finding, error) {
			return []extcheck.Finding{{Message: "check.secrets.token_like"}}, nil
		},
		"another rule's name": func(extcheck.Input) ([]extcheck.Finding, error) {
			return []extcheck.Finding{{Rule: "secrets", Message: "check.broken.x"}}, nil
		},
	}
	for name, fn := range cases {
		for _, sev := range []extcheck.Severity{extcheck.MustFix, extcheck.Advisory} {
			t.Run(name+"/"+sev.String(), func(t *testing.T) {
				r := fakeRule{id: "broken", severity: sev, check: fn}
				got, err := engine(t, r).Check(context.Background(), skill, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(got.Findings) != 1 {
					t.Fatalf("findings = %+v, want exactly the stand-in for the failed rule", got.Findings)
				}
				f := got.Findings[0]
				if f.Rule != "broken" || f.Severity != sev || f.Message != MsgRuleFailed || f.Params["rule"] != "broken" {
					t.Errorf("finding = %+v", f)
				}
				if sev == extcheck.MustFix && got.Passed() {
					t.Error("a must-fix rule that could not finish counts as passed")
				}
			})
		}
	}
}

func TestOnlyRulesForTheKindRun(t *testing.T) {
	ran := map[string]bool{}
	mk := func(id string, kinds ...string) extcheck.Rule {
		return fakeRule{id: id, kinds: kinds, check: func(extcheck.Input) ([]extcheck.Finding, error) {
			ran[id] = true
			return nil, nil
		}}
	}
	got, err := engine(t, mk("for-mcp", resource.KindMCP), mk("for-skills", resource.KindSkill)).Check(context.Background(), skill, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ran["for-mcp"] || !ran["for-skills"] || !slices.Equal(got.Rules, []string{"for-skills"}) {
		t.Errorf("ran %v, result lists %v", ran, got.Rules)
	}
}

func TestAnEndedContextIsAnErrorNotAResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := fakeRule{id: "slow", check: func(extcheck.Input) ([]extcheck.Finding, error) {
		cancel()
		return nil, context.Canceled
	}}
	if _, err := engine(t, r, fakeRule{id: "next", check: func(extcheck.Input) ([]extcheck.Finding, error) { return nil, nil }}).Check(ctx, skill, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("Check after the context ended: error %v", err)
	}
}

func TestMalformedPathsAreRefused(t *testing.T) {
	for _, files := range []map[string][]byte{
		{"../outside": nil},
		{"/etc/passwd": nil},
		{`scripts\run.sh`: nil},
		{"./SKILL.md": nil},
		{"a": nil, "a/b": nil},
	} {
		res := Resource{Ref: skill.Ref, Files: files}
		if _, err := engine(t).Check(context.Background(), res, nil); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("files %v: error %v", slices.Sorted(maps.Keys(files)), err)
		}
	}
}

func TestCheckAllComparesEachResourceWithTheOthers(t *testing.T) {
	other := Resource{
		Ref:   resource.Ref{Kind: resource.KindMCP, Code: "jira"},
		Meta:  resource.Meta{Name: "Jira", Summary: "Read and write issues"},
		Files: map[string][]byte{"jira.yaml": []byte("meta: {name: Jira}\n")},
	}
	var seen = map[string]extcheck.Peers{}
	r := fakeRule{id: "peers", check: func(in extcheck.Input) ([]extcheck.Finding, error) {
		seen[in.Ref.Code] = in.Peers
		return nil, nil
	}}
	results, err := engine(t, r).CheckAll(context.Background(), []Resource{skill, other})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Ref != skill.Ref || results[1].Ref != other.Ref {
		t.Fatalf("results = %+v", results)
	}
	if p := seen["unit-testing"]; len(p) != 1 || p[0].Ref != other.Ref || p[0].Description != "Read and write issues" {
		t.Errorf("peers of the skill = %+v", p)
	}
	if p := seen["jira"]; len(p) != 1 || p[0].Ref != skill.Ref || p[0].Description != "Write unit tests when a change needs them" {
		t.Errorf("peers of the MCP server = %+v", p)
	}
}

func TestDescription(t *testing.T) {
	cases := map[string]string{
		"---\nname: a\ndescription: Use it when tests fail\n---\nbody\n":          "Use it when tests fail",
		"\xef\xbb\xbf---\r\nname: a\r\ndescription: With CRLF and BOM\r\n---\r\n": "With CRLF and BOM",
		"no front matter\n":                    "",
		"---\nname: a\ndescription: [unclosed": "",
		"---\ndescription: : :\n---\n":         "",
	}
	for text, want := range cases {
		res := Resource{Ref: skill.Ref, Files: map[string][]byte{"SKILL.md": []byte(text)}}
		if got := Description(res); got != want {
			t.Errorf("Description of %q = %q, want %q", text, got, want)
		}
	}
	if got := Description(Resource{Ref: skill.Ref}); got != "" {
		t.Errorf("a skill without SKILL.md has the description %q", got)
	}
	if !strings.Contains(Description(Resource{Ref: resource.Ref{Kind: resource.KindMCP}, Meta: resource.Meta{Summary: "s"}}), "s") {
		t.Error("an MCP server's description is not its summary")
	}
}
