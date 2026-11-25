// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package examples holds no code: these tests check the example resource repositories against
// the built-in kinds, the built-in rule set and the schemas, once through the engine and once
// through the command a person runs, rgd check.
package examples

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v4"

	_ "github.com/code2rich/rigdepot/builtin/all"
	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/internal/check"
	"github.com/code2rich/rigdepot/internal/cli"
	"github.com/code2rich/rigdepot/internal/repoformat"
	"github.com/code2rich/rigdepot/internal/testenv"
	"github.com/code2rich/rigdepot/schema"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func engine(t *testing.T) *check.Engine {
	t.Helper()
	e, err := check.New(extcheck.DefaultRuleset())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// checkRepo reads a repository as the programs do, with the built-in kinds, and checks it: the
// resources the reader accepted through the rules, the ones it refused as findings of the
// structure rule.
func checkRepo(t *testing.T, dir string) ([]check.Resource, []check.Result) {
	t.Helper()
	repo, err := repoformat.Read(os.DirFS(dir), repoformat.Options{})
	if err != nil {
		t.Fatal(err)
	}
	located, err := check.FromRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	all := make([]check.Resource, len(located))
	for i, l := range located {
		all[i] = l.Resource
	}
	e := engine(t)
	results, err := e.CheckAll(context.Background(), all)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range repo.Failed {
		results = append(results, check.Refused(f, e.Version()))
	}
	for _, n := range repo.Notices {
		t.Errorf("%s: %s", dir, n)
	}
	return all, results
}

func describe(r check.Result) string {
	var b strings.Builder
	for _, f := range r.Findings {
		b.WriteString("\n  " + r.Ref.String() + " " + f.File + ":" + strconv.Itoa(f.Line) + " " + f.Severity.String() + " " + f.Message)
	}
	return b.String()
}

// The example repository has nothing for any rule to report, and every rule had something of
// its kind to look at.
func TestResourcesPassEveryRule(t *testing.T) {
	all, results := checkRepo(t, "resources")
	var codes []string
	for _, r := range all {
		codes = append(codes, r.Ref.String())
	}
	want := []string{"agent/backend-developer", "workflow/change-to-merge", "skill/code-review", "knowledge/eng-handbook",
		"mcp/gitlab", "mcp/jira", "skill/release-notes", "skill/unit-testing"} // by code
	if !slices.Equal(codes, want) {
		t.Errorf("resources = %v, want %v", codes, want)
	}
	ran := map[string]bool{}
	for _, r := range results {
		if r.RulesetVersion < 1 {
			t.Errorf("%s: result without a rule set version", r.Ref)
		}
		if len(r.Findings) > 0 {
			t.Errorf("%s has findings:%s", r.Ref, describe(r))
		}
		for _, id := range r.Rules {
			ran[id] = true
		}
	}
	for _, rule := range extcheck.DefaultRuleset().Rules {
		if !ran[rule.ID()] {
			t.Errorf("no example resource is of a kind the rule %q checks", rule.ID())
		}
	}
}

// Each directory of resources-broken is hit by exactly the rule it is named after, and there is
// one for every rule.
func TestBrokenHitsExactlyTheRuleInItsName(t *testing.T) {
	entries, err := os.ReadDir("resources-broken")
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirs = append(dirs, e.Name())
		t.Run(e.Name(), func(t *testing.T) {
			_, results := checkRepo(t, filepath.Join("resources-broken", e.Name()))
			hit := map[string]bool{}
			var all string
			for _, r := range results {
				for _, f := range r.Findings {
					hit[f.Rule] = true
					if f.Message == check.MsgRuleFailed {
						t.Errorf("%s: the rule %s did not finish: %v", r.Ref, f.Rule, f.Params["error"])
					}
				}
				all += describe(r)
			}
			var rules []string
			for id := range hit {
				rules = append(rules, id)
			}
			sort.Strings(rules)
			if !slices.Equal(rules, []string{e.Name()}) {
				t.Errorf("hit by %v, want exactly [%s]:%s", rules, e.Name(), all)
			}
		})
	}
	for _, rule := range extcheck.DefaultRuleset().Rules {
		if !slices.Contains(dirs, rule.ID()) {
			t.Errorf("resources-broken has no directory for the rule %q", rule.ID())
		}
	}
}

// The example files are valid against the published schemas: rigdepot.yaml, every meta.yaml and
// the meta of every single-file resource.
func TestExamplesMatchTheSchemas(t *testing.T) {
	repo, meta := compile(t, schema.Repo), compile(t, schema.Meta)
	agent := compileKind(t, "agent")
	checked := 0
	for _, root := range []string{"resources", "resources-broken"} {
		// A skill without meta.yaml is refused when the repository is read, before any rule sees
		// it; the broken examples must break their rule, not that.
		skills, err := fs.Glob(os.DirFS(root), "*/skills/*")
		if err != nil {
			t.Fatal(err)
		}
		more, _ := fs.Glob(os.DirFS(root), "skills/*")
		for _, dir := range append(skills, more...) {
			if _, err := os.Stat(filepath.Join(root, dir, "meta.yaml")); err != nil {
				t.Errorf("%s/%s has no meta.yaml", root, dir)
			}
		}
		err = fs.WalkDir(os.DirFS(root), ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || path.Ext(p) != ".yaml" {
				return err
			}
			doc := loadYAML(t, filepath.Join(root, p))
			var sch *jsonschema.Schema
			switch {
			case path.Base(p) == "rigdepot.yaml":
				sch = repo
			case path.Base(p) == "meta.yaml":
				sch = meta
			case path.Base(p) == "agent.yaml":
				sch = agent
			case hasTop(p, "mcp", "workflows", "knowledge"):
				obj, _ := doc.(map[string]any)
				doc, sch = obj["meta"], meta
			default:
				t.Errorf("%s/%s: no schema known for this file", root, p)
				return nil
			}
			if err := sch.Validate(doc); err != nil {
				t.Errorf("%s/%s: %v", root, p, err)
			}
			checked++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked < 10 {
		t.Errorf("only %d files were validated", checked)
	}
}

// hasTop reports whether p is in one of the given top-level directories of a repository, which
// may itself be one directory down (resources-broken/<rule>/…).
func hasTop(p string, dirs ...string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(p, d+"/") || strings.Contains(p, "/"+d+"/") {
			return true
		}
	}
	return false
}

// compileKind compiles the published schema of a directory-shaped kind's own file, which refers
// to nothing else.
func compileKind(t *testing.T, kind string) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "schema", "repo", "v1", "kinds", kind+".schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	url, _ := doc.(map[string]any)["$id"].(string)
	c := jsonschema.NewCompiler()
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

func compile(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	var id string
	for _, n := range []string{schema.Repo, schema.Meta} {
		data, err := fs.ReadFile(schema.FS(), n)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		url, _ := doc.(map[string]any)["$id"].(string)
		if err := c.AddResource(url, doc); err != nil {
			t.Fatal(err)
		}
		if n == name {
			id = url
		}
	}
	sch, err := c.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

// loadYAML reads a YAML file into the JSON data model the validator takes.
func loadYAML(t *testing.T, file string) any {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	js, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(js))
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return doc
}

// rgdCheck runs "rgd check --json <dir>" in this process, with the built-in kinds and rules, in
// the temporary home directory.
func rgdCheck(t *testing.T, dir string, lang string) (int, map[string]any, string) {
	t.Helper()
	h := testenv.Current(t).Home
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := cli.Run(args, cli.Env{
			Stdin:           strings.NewReader(""),
			Stdout:          &stdout,
			Stderr:          &stderr,
			Getenv:          func(string) string { return "" },
			HomeDir:         func() (string, error) { return h, nil },
			SystemLanguages: func() []string { return []string{lang} },
			IsTerminal:      func(any) bool { return false },
			GOOS:            runtime.GOOS,
		})
		return code, stdout.String(), stderr.String()
	}
	code, out, errOut := run("check", "--json", dir)
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("rgd check --json %s: %v\n%s\n%s", dir, err, out, errOut)
	}
	human, text, _ := run("check", dir)
	if human != code {
		t.Errorf("rgd check %s: exit code %d, with --json %d", dir, human, code)
	}
	return code, doc, text
}

// findingsOf returns every finding in a check result, as "<path> <rule> <severity> <key>".
func findingsOf(doc map[string]any) (rules []string, lines []string) {
	resources, _ := doc["resources"].([]any)
	for _, r := range resources {
		res := r.(map[string]any)
		checks := res["checks"].(map[string]any)
		for _, f := range checks["findings"].([]any) {
			f := f.(map[string]any)
			if !slices.Contains(rules, f["rule"].(string)) {
				rules = append(rules, f["rule"].(string))
			}
			lines = append(lines, fmt.Sprintf("%v %v %v %v", res["path"], f["rule"], f["severity"], f["key"]))
		}
	}
	sort.Strings(rules)
	return rules, lines
}

// "rgd check examples/resources" finds nothing, and "rgd check examples/resources-broken/<rule>"
// finds exactly that rule, with exit code 4 for a rule that must be fixed and 0 for an advisory
// one. Every message is translated.
func TestRgdCheck(t *testing.T) {
	code, doc, text := rgdCheck(t, "resources", "en")
	if rules, lines := findingsOf(doc); code != 0 || len(rules) != 0 {
		t.Errorf("rgd check resources: exit code %d, findings:\n%s", code, strings.Join(lines, "\n"))
	}
	if s := doc["summary"].(map[string]any); s["resources"] != 8.0 || s["passed"] != true || s["notices"] != 0.0 {
		t.Errorf("summary %v", s)
	}
	if !strings.Contains(text, "Nothing to fix.") {
		t.Errorf("human output:\n%s", text)
	}

	entries, err := os.ReadDir("resources-broken")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			rule, ok := extcheck.Lookup(e.Name())
			if !ok {
				t.Fatalf("there is no rule %q", e.Name())
			}
			want := 0
			if rule.Severity() == extcheck.MustFix {
				want = 4
			}
			for _, lang := range []string{"en", "zh-CN"} {
				code, doc, text := rgdCheck(t, filepath.Join("resources-broken", e.Name()), lang)
				rules, lines := findingsOf(doc)
				if code != want || !slices.Equal(rules, []string{e.Name()}) {
					t.Errorf("%s: exit code %d (want %d), rules %v:\n%s", lang, code, want, rules, strings.Join(lines, "\n"))
				}
				if strings.Contains(text, "check."+e.Name()+".") || strings.Contains(text, "{{") {
					t.Errorf("%s: a message is not translated:\n%s", lang, text)
				}
			}
		})
	}
}
