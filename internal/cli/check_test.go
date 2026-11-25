// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/code2rich/rigdepot/api"
	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// The real kinds and rules live in builtin/, which the core must not import; examples/ runs the
// command with them. Here the test binary registers stand-ins: a skill kind that refuses a
// SKILL.md whose name is not the directory's, an MCP kind of single YAML files, a must-fix rule
// that flags "FIXME-SECRET" and an advisory rule that flags "TODO-WORDING".
func init() {
	kind.Register(stubSkill{})
	kind.Register(stubMCP{})
	extcheck.SetVersion(3)
	extcheck.Register(stubRule{id: "flag", severity: extcheck.MustFix, word: "FIXME-SECRET"})
	extcheck.Register(stubRule{id: "wording", severity: extcheck.Advisory, word: "TODO-WORDING"})
}

type stubSkill struct{}

func (stubSkill) ID() string          { return resource.KindSkill }
func (stubSkill) Layout() kind.Layout { return kind.Layout{Dir: "skills"} }
func (stubSkill) Schema() []byte      { return nil }
func (stubSkill) Forms() []kind.Form  { return nil }
func (stubSkill) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	data, err := fs.ReadFile(files, "SKILL.md")
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(data), "\nname: "+ref.Code+"\n") {
		return nil, fmt.Errorf("the name in SKILL.md is not %q", ref.Code)
	}
	return ref, nil
}

type stubMCP struct{}

func (stubMCP) ID() string          { return resource.KindMCP }
func (stubMCP) Layout() kind.Layout { return kind.Layout{Dir: "mcp", File: true, Ext: ".yaml"} }
func (stubMCP) Schema() []byte {
	return []byte(`{"type":"object","required":["meta","server"],"properties":{"meta":{"type":"object"},"server":{"type":"object"}}}`)
}
func (stubMCP) Forms() []kind.Form                                 { return nil }
func (stubMCP) Parse(ref resource.Ref, _ fs.FS) (kind.Spec, error) { return ref, nil }

type stubRule struct {
	id       string
	severity extcheck.Severity
	word     string
}

func (r stubRule) ID() string                  { return r.id }
func (r stubRule) Severity() extcheck.Severity { return r.severity }
func (stubRule) Applies(string) bool           { return true }
func (r stubRule) Check(_ context.Context, in extcheck.Input) ([]extcheck.Finding, error) {
	var found []extcheck.Finding
	err := fs.WalkDir(in.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(in.Files, p)
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, r.word) {
				found = append(found, extcheck.Finding{Severity: r.severity, File: p, Line: i + 1,
					Message: "check." + r.id + ".found", Params: map[string]any{"word": r.word}})
			}
		}
		return err
	})
	return found, err
}

// writeRepo lays out a resource repository under dir.
func writeRepo(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func cleanRepo() map[string]string {
	return map[string]string{
		"rigdepot.yaml":                  "schema: 1\nname: Example\n",
		"README.md":                      "# Example resources\n",
		"skills/unit-testing/SKILL.md":   "---\nname: unit-testing\ndescription: Write unit tests\n---\n\nStep one.\n",
		"skills/unit-testing/meta.yaml":  "name: Unit testing\n",
		"skills/release-notes/SKILL.md":  "---\nname: release-notes\ndescription: Write release notes\n---\n",
		"skills/release-notes/meta.yaml": "name: Release notes\n",
		"mcp/jira.yaml":                  "meta:\n  name: Jira\nserver:\n  type: http\n  url: https://jira.example.com/mcp\n",
	}
}

func (w *world) repo(t *testing.T, change func(map[string]string)) string {
	t.Helper()
	files := cleanRepo()
	if change != nil {
		change(files)
	}
	dir, err := os.MkdirTemp(w.home, "repo-")
	if err != nil {
		t.Fatal(err)
	}
	return writeRepo(t, dir, files)
}

// checkJSON runs rgd check --json and returns the exit code and the document.
func (w *world) checkJSON(t *testing.T, args ...string) (int, map[string]any) {
	t.Helper()
	args = append([]string{"check", "--json"}, args...)
	r := w.run(args...)
	return r.code, oneDocument(t, args, r.stdout)
}

type checkedResource struct {
	path, kind, code string
	refused          bool
	findings         []map[string]any
	checks           map[string]any
}

func resourcesOf(t *testing.T, doc map[string]any) map[string]checkedResource {
	t.Helper()
	if doc["schema"] != cliout.Schema("check", 1) {
		t.Fatalf("not a check result: %v", doc)
	}
	out := map[string]checkedResource{}
	list, _ := doc["resources"].([]any)
	for _, item := range list {
		m := item.(map[string]any)
		checks := m["checks"].(map[string]any)
		r := checkedResource{path: m["path"].(string), kind: m["kind"].(string), code: m["code"].(string), checks: checks}
		r.refused, _ = m["refused"].(bool)
		for _, f := range checks["findings"].([]any) {
			r.findings = append(r.findings, f.(map[string]any))
		}
		out[r.path] = r
	}
	return out
}

func summaryOf(doc map[string]any) map[string]any { return doc["summary"].(map[string]any) }

func TestCheckACleanRepository(t *testing.T) {
	w := newWorld(t)
	dir := w.repo(t, nil)
	code, doc := w.checkJSON(t, dir)
	if code != int(cliout.ExitOK) {
		t.Fatalf("exit code %d: %v", code, doc)
	}
	res := resourcesOf(t, doc)
	var paths []string
	for p := range res {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	if want := []string{"mcp/jira.yaml", "skills/release-notes", "skills/unit-testing"}; !slices.Equal(paths, want) {
		t.Errorf("resources %v, want %v", paths, want)
	}
	if r := res["mcp/jira.yaml"]; r.kind != resource.KindMCP || r.code != "jira" || len(r.findings) != 0 {
		t.Errorf("jira: %+v", r)
	}
	if doc["ruleset"] != 3.0 || !slices.Equal(toStrings(doc["rules"]), []string{"flag", "wording"}) {
		t.Errorf("ruleset %v, rules %v", doc["ruleset"], doc["rules"])
	}
	s := summaryOf(doc)
	if s["resources"] != 3.0 || s["blocking"] != 0.0 || s["advisory"] != 0.0 || s["passed"] != true {
		t.Errorf("summary %v", s)
	}

	r := w.run("check", dir)
	if r.code != 0 || !strings.Contains(r.stdout, "Checked 3 resources in "+dir+" against rule set v3") ||
		!strings.Contains(r.stdout, "Nothing to fix.") || r.stderr != "" {
		t.Errorf("human output (exit %d):\n%s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
}

func toStrings(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, s := range list {
		out = append(out, s.(string))
	}
	return out
}

// A must-fix finding is exit code 4; an advisory one is not, unless --strict.
func TestCheckExitCodes(t *testing.T) {
	w := newWorld(t)
	dir := w.repo(t, func(f map[string]string) {
		f["skills/unit-testing/references/a.md"] = "line one\napi_key = FIXME-SECRET\n"
		f["skills/release-notes/SKILL.md"] += "TODO-WORDING\n"
	})
	code, doc := w.checkJSON(t, dir)
	if code != int(cliout.ExitCheckFailed) {
		t.Fatalf("exit code %d, want 4", code)
	}
	res := resourcesOf(t, doc)
	f := res["skills/unit-testing"].findings
	if len(f) != 1 || f[0]["rule"] != "flag" || f[0]["severity"] != "must_fix" || f[0]["file"] != "references/a.md" ||
		f[0]["line"] != 2.0 || f[0]["key"] != "check.flag.found" || !strings.HasPrefix(f[0]["id"].(string), "f-flag-") {
		t.Errorf("must-fix finding: %v", f)
	}
	if f := res["skills/release-notes"].findings; len(f) != 1 || f[0]["severity"] != "advisory" {
		t.Errorf("advisory finding: %v", f)
	}
	if c := res["skills/unit-testing"].checks; c["blocking"] != 1.0 || c["advisory"] != 0.0 || c["ruleset"] != 3.0 {
		t.Errorf("report: %v", c)
	}
	if s := summaryOf(doc); s["blocking"] != 1.0 || s["advisory"] != 1.0 || s["passed"] != false {
		t.Errorf("summary %v", s)
	}

	r := w.run("check", dir)
	if r.code != int(cliout.ExitCheckFailed) || !strings.Contains(r.stdout, "x references/a.md:2  flag") ||
		!strings.Contains(r.stdout, "! SKILL.md:5  wording") || !strings.Contains(r.stdout, "1 must fix · 1 advisory") ||
		!strings.Contains(r.stdout, "cannot be published") || r.stderr != "" {
		t.Errorf("human output (exit %d):\n%s\nstderr: %s", r.code, r.stdout, r.stderr)
	}

	advisoryOnly := newWorld(t)
	dir = advisoryOnly.repo(t, func(f map[string]string) { f["skills/release-notes/SKILL.md"] += "TODO-WORDING\n" })
	if code, _ := advisoryOnly.checkJSON(t, dir); code != int(cliout.ExitOK) {
		t.Errorf("advisory only: exit code %d, want 0", code)
	}
	if code, _ := advisoryOnly.checkJSON(t, "--strict", dir); code != int(cliout.ExitCheckFailed) {
		t.Errorf("advisory only with --strict: exit code %d, want 4", code)
	}
}

// What the repository reader refuses never reaches a rule; it is reported as must-fix findings
// of the structure rule, with paths relative to the resource.
func TestCheckReportsRefusedResourcesAsStructure(t *testing.T) {
	w := newWorld(t)
	dir := w.repo(t, func(f map[string]string) {
		f["skills/unit-testing/SKILL.md"] = "---\nname: unit-test\ndescription: Write unit tests\n---\n" // name differs
		delete(f, "skills/release-notes/meta.yaml")
		f["mcp/broken.yaml"] = "meta:\n  name: Broken\n"                                           // no server
		f["skills/release-notes/SKILL.md"] = "FIXME-SECRET\n" + f["skills/release-notes/SKILL.md"] // never seen by a rule
	})
	code, doc := w.checkJSON(t, dir)
	if code != int(cliout.ExitCheckFailed) {
		t.Fatalf("exit code %d, want 4", code)
	}
	res := resourcesOf(t, doc)
	want := map[string]struct{ key, file string }{
		"skills/unit-testing":  {"check.structure.repo.parse", ""},
		"skills/release-notes": {"check.structure.repo.missing_meta", "meta.yaml"},
		"mcp/broken.yaml":      {"check.structure.repo.schema", "broken.yaml"},
	}
	for p, w := range want {
		r := res[p]
		if !r.refused || len(r.findings) == 0 {
			t.Errorf("%s: %+v", p, r)
			continue
		}
		f := r.findings[0]
		if f["rule"] != "structure" || f["severity"] != "must_fix" || f["key"] != w.key || (f["file"] != nil && f["file"] != w.file) ||
			(w.file != "" && f["file"] != w.file) || f["message"] == "" || f["message"] == w.key {
			t.Errorf("%s: %v, want %s in %q", p, f, w.key, w.file)
		}
	}
	if r := res["mcp/jira.yaml"]; r.refused || len(r.findings) != 0 {
		t.Errorf("the good resource: %+v", r)
	}
	if s := summaryOf(doc); s["refused"] != 3.0 || s["blocking"] != 3.0 {
		t.Errorf("summary %v", s)
	}
	// --rules does not hide them: a refused resource was not checked at all.
	if code, doc := w.checkJSON(t, "--rules", "wording", dir); code != int(cliout.ExitCheckFailed) || summaryOf(doc)["refused"] != 3.0 {
		t.Errorf("--rules wording: exit code %d, %v", code, summaryOf(doc))
	}

	r := w.run("--lang", "zh-CN", "check", dir)
	for _, s := range []string{"skills/release-notes", "缺少元数据", "必须修复 3 项", "不能发布"} {
		if !strings.Contains(r.stdout, s) {
			t.Errorf("the Chinese output lacks %q:\n%s", s, r.stdout)
		}
	}
}

func TestCheckUnsafePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symbolic link needs a privilege on Windows")
	}
	w := newWorld(t)
	dir := w.repo(t, nil)
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "skills", "unit-testing", "hosts")); err != nil {
		t.Fatal(err)
	}
	code, doc := w.checkJSON(t, dir)
	r := resourcesOf(t, doc)["skills/unit-testing"]
	if code != int(cliout.ExitCheckFailed) || !r.refused || r.findings[0]["key"] != "check.structure.repo.unsafe_path" || r.findings[0]["file"] != "hosts" {
		t.Errorf("exit code %d, %+v", code, r)
	}
}

// Advisory notices of the reader: about a resource's file, a finding of that resource; about the
// repository, a notice. They block nothing.
func TestCheckNotices(t *testing.T) {
	w := newWorld(t)
	dir := w.repo(t, func(f map[string]string) {
		f["skills/unit-testing/meta.yaml"] = "name: Unit testing\nsummray: typo\n"
		f["prompts/a.md"] = "not a kind\n"
	})
	code, doc := w.checkJSON(t, dir)
	if code != int(cliout.ExitOK) {
		t.Fatalf("exit code %d", code)
	}
	f := resourcesOf(t, doc)["skills/unit-testing"].findings
	if len(f) != 1 || f[0]["key"] != "check.structure.repo.unknown_field" || f[0]["severity"] != "advisory" || f[0]["file"] != "meta.yaml" {
		t.Errorf("findings %v", f)
	}
	notices, _ := doc["notices"].([]any)
	if len(notices) != 1 || notices[0].(map[string]any)["code"] != "unknown-dir" || notices[0].(map[string]any)["file"] != "prompts" {
		t.Errorf("notices %v", notices)
	}
	if code, _ := w.checkJSON(t, "--strict", dir); code != int(cliout.ExitCheckFailed) {
		t.Errorf("--strict: exit code %d", code)
	}
}

// A path below the repository's root checks what is there: a kind's directory, one resource, or
// the resource a file belongs to. The other resources are still its peers.
func TestCheckPartOfARepository(t *testing.T) {
	w := newWorld(t)
	dir := w.repo(t, func(f map[string]string) {
		f["skills/unit-testing/refs/x.md"] = "FIXME-SECRET\n"
	})
	cases := map[string][]string{
		"skills":                        {"skills/release-notes", "skills/unit-testing"},
		"skills/release-notes":          {"skills/release-notes"},
		"skills/unit-testing/refs/x.md": {"skills/unit-testing"},
		"mcp/jira.yaml":                 {"mcp/jira.yaml"},
	}
	for sub, want := range cases {
		code, doc := w.checkJSON(t, filepath.Join(dir, filepath.FromSlash(sub)))
		var got []string
		for p := range resourcesOf(t, doc) {
			got = append(got, p)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("check %s: %v, want %v", sub, got, want)
		}
		wantCode := cliout.ExitOK
		if slices.Contains(want, "skills/unit-testing") {
			wantCode = cliout.ExitCheckFailed
		}
		if code != int(wantCode) {
			t.Errorf("check %s: exit code %d, want %d", sub, code, wantCode)
		}
	}
	for _, sub := range []string{"README.md", "skills/missing"} {
		args := []string{"--json", "check", filepath.Join(dir, filepath.FromSlash(sub))}
		r := w.run(args...)
		doc := oneDocument(t, args, r.stdout)
		errDoc, _ := doc["error"].(map[string]any)
		want := "no_resource"
		if sub == "skills/missing" {
			want = "unreadable"
		}
		if r.code != int(cliout.ExitFailure) || errDoc["code"] != want {
			t.Errorf("check %s: exit code %d, %v", sub, r.code, doc)
		}
	}
}

// The default is the current directory, and the repository is found above it.
func TestCheckFindsTheRepositoryFromTheWorkingDirectory(t *testing.T) {
	w := newWorld(t)
	dir := w.repo(t, nil)
	t.Chdir(filepath.Join(dir, "skills", "unit-testing"))
	code, doc := w.checkJSON(t)
	if res := resourcesOf(t, doc); code != 0 || len(res) != 1 || doc["source"] != "." {
		t.Errorf("exit code %d, %v", code, doc)
	}
}

func TestCheckErrors(t *testing.T) {
	w := newWorld(t)
	dir := w.repo(t, nil)
	notRepo := t.TempDir()
	badConfig := writeRepo(t, t.TempDir(), map[string]string{"rigdepot.yaml": "schema: 1\nstages: nope\n"})
	newer := writeRepo(t, t.TempDir(), map[string]string{"rigdepot.yaml": "schema: 9\n"})
	tests := []struct {
		name string
		args []string
		exit cliout.ExitCode
		code string
	}{
		{"not in a repository", []string{notRepo}, cliout.ExitFailure, "not_a_repository"},
		{"a path that does not exist", []string{filepath.Join(notRepo, "nothing")}, cliout.ExitFailure, "unreadable"},
		{"rigdepot.yaml cannot be used", []string{badConfig}, cliout.ExitCheckFailed, "repo_config_invalid"},
		{"a newer format", []string{newer}, cliout.ExitIncompatible, "incompatible"},
		{"an unknown rule", []string{"--rules", "flag,nope", dir}, cliout.ExitUsage, "unknown_rule"},
		{"two paths", []string{dir, dir}, cliout.ExitUsage, "argument_count"},
		{"a path and --repo", []string{"--repo", dir, dir}, cliout.ExitUsage, "repo_and_path"},
		{"--branch without --repo", []string{"--branch", "main", dir}, cliout.ExitUsage, "repo_only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"--json", "check"}, tt.args...)
			r := w.run(args...)
			doc := oneDocument(t, args, r.stdout)
			errDoc, _ := doc["error"].(map[string]any)
			if r.code != int(tt.exit) || errDoc["code"] != tt.code {
				t.Errorf("exit code %d, %v; want %d %s", r.code, doc, tt.exit, tt.code)
			}
		})
	}
	r := w.run("check", badConfig)
	if !strings.Contains(r.stderr, "rigdepot.yaml:2") || !strings.Contains(r.stderr, "stages") {
		t.Errorf("the configuration error does not say where: %s", r.stderr)
	}
}

// --repo reads a repository by its address, as committed, through git.
func TestCheckRepoByAddress(t *testing.T) {
	git := testenv.NeedGit(t)
	w := newWorld(t)
	w.env["RGD_GIT"] = git
	dir := w.repo(t, func(f map[string]string) {
		f["skills/unit-testing/SKILL.md"] += "FIXME-SECRET\n"
	})
	gitIn := func(args ...string) string {
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=author@example.com",
			"GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=author@example.com")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, stderr.String())
		}
		return strings.TrimSpace(string(out))
	}
	gitIn("init", "-q", "-b", "main")
	gitIn("add", ".")
	gitIn("commit", "-q", "-m", "first")
	commit := gitIn("rev-parse", "HEAD")
	// Not committed, so not checked.
	if err := os.WriteFile(filepath.Join(dir, "mcp", "jira.yaml"), []byte("FIXME-SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, doc := w.checkJSON(t, "--repo", dir, "--branch", "main")
	if code != int(cliout.ExitCheckFailed) || doc["commit"] != commit || doc["source"] != dir {
		t.Fatalf("exit code %d, %v", code, doc)
	}
	res := resourcesOf(t, doc)
	if len(res["skills/unit-testing"].findings) != 1 || len(res["mcp/jira.yaml"].findings) != 0 {
		t.Errorf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(w.dataDir(), "cache", "repos")); err != nil {
		t.Errorf("the clone is not in the cache: %v", err)
	}

	args := []string{"--json", "check", "--repo", dir, "--branch", "no-such-branch"}
	r := w.run(args...)
	if errDoc, _ := oneDocument(t, args, r.stdout)["error"].(map[string]any); r.code != int(cliout.ExitFailure) || errDoc["code"] != "repo_no_ref" {
		t.Errorf("unknown branch: exit code %d, %s", r.code, r.stdout)
	}
}

// What rgd check --json says about each resource is what the server stores for a draft: it must
// match CheckReport in the API contract.
func TestCheckOutputMatchesTheContract(t *testing.T) {
	loader := openapi3.NewLoader()
	spec, err := loader.LoadFromData(api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := spec.Components.Schemas["CheckReport"]
	if ref == nil || ref.Value == nil {
		t.Fatal("the contract has no CheckReport")
	}
	w := newWorld(t)
	dir := w.repo(t, func(f map[string]string) {
		f["skills/unit-testing/references/a.md"] = "FIXME-SECRET\nTODO-WORDING\n"
		f["skills/unit-testing/meta.yaml"] += "summray: typo\n"
		f["mcp/broken.yaml"] = "meta:\n  name: Broken\n"
		f["skills/bad-name/SKILL.md"] = "---\nname: other\n---\n"
		f["skills/bad-name/meta.yaml"] = "name: Bad\n"
	})
	_, doc := w.checkJSON(t, dir)
	checked := 0
	for p, r := range resourcesOf(t, doc) {
		// Through JSON again, so that numbers are float64 as the validator expects.
		var v any
		data, _ := json.Marshal(r.checks)
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		if err := ref.Value.VisitJSON(v); err != nil {
			t.Errorf("%s: the checks do not match CheckReport: %v\n%s", p, err, data)
		}
		checked += len(r.findings)
	}
	if checked < 5 {
		t.Errorf("only %d findings were validated", checked)
	}

	// The validator itself is not lenient: a number as the severity is refused.
	bad := map[string]any{"ruleset": 1.0, "blocking": 0.0, "advisory": 0.0, "findings": []any{
		map[string]any{"id": "f", "rule": "r", "severity": 1.0, "message": "m"},
	}}
	if err := ref.Value.VisitJSON(bad); err == nil {
		t.Error("a severity given as a number passed the contract")
	}
}
