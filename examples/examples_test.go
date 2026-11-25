// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package examples holds no code: these tests check the example resource repositories against
// the built-in rule set and the schemas. "rgd check examples/resources" says the same thing once
// the command exists; until then, and afterwards as a guard, this runs the engine directly.
package examples

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
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
	"github.com/code2rich/rigdepot/internal/check/repodir"
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

func checkRepo(t *testing.T, dir string) ([]check.Resource, []check.Result) {
	t.Helper()
	all, err := repodir.Load(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	results, err := engine(t).CheckAll(context.Background(), all)
	if err != nil {
		t.Fatal(err)
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
	want := []string{"mcp/gitlab", "mcp/jira", "skill/code-review", "skill/release-notes", "skill/unit-testing"}
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
			case strings.HasPrefix(p, "mcp/") || strings.Contains(p, "/mcp/"):
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
