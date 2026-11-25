// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package kindtest

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing/fstest"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/repoformat"
)

// defaultConfig is the repository configuration the fixtures are read with when dir has no
// rigdepot.yaml of its own: the default stages, no secrets, no products.
const defaultConfig = "schema: 1\n"

// fieldSep separates the field from the code in the name of an invalid fixture.
const fieldSep = "--"

// The options every resource is rendered with. Rendering with two different prefixes shows
// that a kind takes the prefix from the options and spells out no prefix of its own.
var (
	optA = kind.RenderOptions{Prefix: "acme-", CLI: "acme"}
	optB = kind.RenderOptions{Prefix: "zz-", CLI: "zz"}
)

// tools are the tools every valid resource is rendered for, from one that takes everything to
// one that takes nothing.
var tools = []struct {
	name string
	caps adapter.Capabilities
}{
	{"everything", adapter.Capabilities{Skills: true, Commands: true, Agents: true, Hooks: true, Plugin: true,
		MCP: adapter.MCPCaps{Stdio: true, HTTP: true, EnvRef: true, ToolSearch: true}}},
	{"skills only", adapter.Capabilities{Skills: true}},
	{"MCP over stdio only", adapter.Capabilities{MCP: adapter.MCPCaps{Stdio: true, EnvRef: true}}},
	{"MCP over HTTP only", adapter.Capabilities{MCP: adapter.MCPCaps{HTTP: true, EnvRef: true}}},
	{"skills and MCP without environment references", adapter.Capabilities{Skills: true, MCP: adapter.MCPCaps{Stdio: true, HTTP: true}}},
	{"commands and agents only", adapter.Capabilities{Commands: true, Agents: true}},
	{"nothing", adapter.Capabilities{}},
}

// messageKey is the shape of a message key, such as "workflow.as_skill".
var messageKey = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)+$`)

// secretRef is how a resource file refers to a personal secret. In an MCP server, once a kind
// has parsed it, the reference is structure (adapter.Value.Secret), never text. (The files of a
// skill are delivered as they are; a .env.example in one may well declare such a reference.)
const secretRef = "${secret:"

func runSuite(r reporter, k kind.Kind, dir string) {
	r.Helper()
	r.run("schema", func(r reporter) { checkKindSchema(r, k) })
	forms := k.Forms()
	r.run("forms", func(r reporter) { checkKindForms(r, forms) })

	l := k.Layout()
	cfg := []byte(defaultConfig)
	if data, err := os.ReadFile(filepath.Join(dir, brand.RepoConfig)); err == nil {
		cfg = data
	} else if !errors.Is(err, fs.ErrNotExist) {
		r.fatal(checkFixtures, "%v", err)
	}
	for _, sub := range []string{"valid", "invalid"} {
		fixtures, err := listFixtures(filepath.Join(dir, sub), l)
		if err != nil {
			r.fail(checkFixtures, "%v", err)
			continue
		}
		if len(fixtures) == 0 {
			r.fail(checkFixtures, "%s has no fixtures; every kind needs at least one resource that must be read and one that must be refused", filepath.Join(dir, sub))
		}
		for _, name := range fixtures {
			r.run(sub+"/"+name, func(r reporter) {
				if sub == "valid" {
					runValid(r, k, forms, cfg, filepath.Join(dir, sub, name), name)
				} else {
					runInvalid(r, k, cfg, filepath.Join(dir, sub, name), name)
				}
			})
		}
	}
}

func checkKindSchema(r reporter, k kind.Kind) {
	if len(bytes.TrimSpace(k.Schema())) == 0 {
		r.fatal(checkSchema, "Schema returns nothing; every kind describes the shape of its file, or of the structured part of its directory")
	}
	if err := repoformat.CheckKindSchema(k); err != nil {
		r.fail(checkSchema, "%v", err)
	}
}

func checkKindForms(r reporter, forms []kind.Form) {
	if len(forms) == 0 {
		r.fail(checkForms, "Forms is empty: a resource of this kind could never be delivered")
	}
	for i, f := range forms {
		if f.Render == nil {
			r.fail(checkForms, "form %d has no Render", i)
		}
		if f.Degraded != "" && !messageKey.MatchString(f.Degraded) {
			r.fail(checkForms, "form %d: Degraded is %q, which is not a message key such as \"workflow.as_skill\"", i, f.Degraded)
		}
	}
}

// listFixtures returns the names of the fixtures in dir, sorted. A fixture of a directory-shaped
// kind is a directory, of a single-file kind a file with the kind's extension; anything else
// in dir is a mistake, because it would never be run. Names starting with a dot are skipped.
func listFixtures(dir string, l kind.Layout) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out, wrong []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "."):
		case l.File && e.Type().IsRegular() && strings.HasSuffix(name, l.Ext):
			out = append(out, name)
		case !l.File && e.IsDir():
			out = append(out, name)
		default:
			wrong = append(wrong, name)
		}
	}
	if len(wrong) > 0 {
		shape := "a directory"
		if l.File {
			shape = "a " + l.Ext + " file"
		}
		return out, fmt.Errorf("%s: %s: a fixture of this kind is %s", dir, strings.Join(wrong, ", "), shape)
	}
	return out, nil
}

// readFixture reads one fixture the way the core reads a repository: a repository with the
// configuration and the fixture as its only resource, under the given code.
func readFixture(r reporter, k kind.Kind, cfg []byte, src, code string) *repoformat.Repo {
	r.Helper()
	l := k.Layout()
	fsys := fstest.MapFS{brand.RepoConfig: {Data: cfg}}
	if l.File {
		data, err := os.ReadFile(src)
		if err != nil {
			r.fatal(checkFixtures, "%v", err)
		}
		fsys[l.Dir+"/"+code+l.Ext] = &fstest.MapFile{Data: data, Mode: 0o644}
	} else {
		err := fs.WalkDir(os.DirFS(src), ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("%s is not a regular file", p)
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(p)))
			if err != nil {
				return err
			}
			fsys[l.Dir+"/"+code+"/"+p] = &fstest.MapFile{Data: data, Mode: info.Mode().Perm()}
			return nil
		})
		if err != nil {
			r.fatal(checkFixtures, "%s: %v", src, err)
		}
	}
	repo, err := repoformat.Read(fsys, repoformat.Options{Kinds: only{k}})
	if err != nil {
		r.fatal(checkFixtures, "the fixture repository cannot be read at all: %v", err)
	}
	return repo
}

// only is a set of kinds with one kind in it.
type only struct{ k kind.Kind }

func (o only) All() []kind.Kind { return []kind.Kind{o.k} }

func describeIssues(repo *repoformat.Repo) string {
	var lines []string
	for _, f := range repo.Failed {
		for _, is := range f.Issues {
			lines = append(lines, "  "+is.String())
		}
	}
	for _, is := range repo.Notices {
		lines = append(lines, "  notice: "+is.String())
	}
	if len(lines) == 0 {
		return "  (nothing reported)"
	}
	return strings.Join(lines, "\n")
}

func runInvalid(r reporter, k kind.Kind, cfg []byte, src, name string) {
	stem := strings.TrimSuffix(name, k.Layout().Ext)
	field, code, ok := strings.Cut(stem, fieldSep)
	if !ok || field == "" || !resource.ValidCode(code) {
		r.fatal(checkFixtures, "an invalid fixture is named <field>%s<code>, such as server.url%ssecret-in-url, so that the suite can check the refusal names the field; %q is not", fieldSep, fieldSep, stem)
	}
	repo := readFixture(r, k, cfg, src, code)
	if len(repo.Resources) > 0 {
		r.fatal(checkInvalid, "the resource was read and parsed; it has to be refused (by the schema or by Parse) because of %s", field)
	}
	if len(repo.Failed) == 0 {
		r.fatal(checkInvalid, "the resource was neither read nor refused:\n%s", describeIssues(repo))
	}
	for _, f := range repo.Failed {
		for _, is := range f.Issues {
			if strings.Contains(is.Field, field) || strings.Contains(is.Message, field) {
				return
			}
		}
	}
	r.fail(checkInvalid, "the resource was refused, but no reason names %s; a person could not tell what to fix:\n%s", field, describeIssues(repo))
}

func runValid(r reporter, k kind.Kind, forms []kind.Form, cfg []byte, src, code string) {
	code = strings.TrimSuffix(code, k.Layout().Ext)
	if !resource.ValidCode(code) {
		r.fatal(checkFixtures, "a valid fixture is named after its code, and %q is not a valid code", code)
	}
	repo := readFixture(r, k, cfg, src, code)
	res, ok := repo.Lookup(code)
	if !ok {
		r.fatal(checkValid, "the resource was not read:\n%s", describeIssues(repo))
	}
	for _, tool := range tools {
		r.run(tool.name, func(r reporter) { checkDelivery(r, res, forms, tool.caps, tool.name == "everything") })
	}
}

// delivery is what the core makes of one resource for one tool: the first form whose needs the
// tool meets and that does not decline, and its artifacts.
type delivery struct {
	form      int // -1: no form fits
	artifacts []adapter.Artifact
}

// deliver walks the forms the way the core does. A failure of Render is reported and ends the
// subtest.
func deliver(r reporter, spec kind.Spec, forms []kind.Form, caps adapter.Capabilities, opt kind.RenderOptions) delivery {
	r.Helper()
	for i, f := range forms {
		if f.Render == nil || !f.Needs.SatisfiedBy(caps) {
			continue
		}
		arts, err := f.Render(spec, caps, opt)
		if errors.Is(err, kind.ErrNotApplicable) {
			continue
		}
		if err != nil {
			r.fatal(checkRender, "form %d failed for a valid resource: %v", i, err)
		}
		if len(arts) == 0 {
			r.fatal(checkRender, "form %d returned no artifacts and no error; a form that does not fit returns kind.ErrNotApplicable", i)
		}
		return delivery{form: i, artifacts: arts}
	}
	return delivery{form: -1}
}

func checkDelivery(r reporter, res repoformat.Resource, forms []kind.Form, caps adapter.Capabilities, everything bool) {
	a := deliver(r, res.Spec, forms, caps, optA)
	again := deliver(r, res.Spec, forms, caps, optA)
	if a.form != again.form || !reflect.DeepEqual(a.artifacts, again.artifacts) {
		r.fail(checkDeterministic, "rendering the same resource twice gave different results: form %d %#v, then form %d %#v",
			a.form, a.artifacts, again.form, again.artifacts)
	}
	if a.form < 0 {
		if everything {
			r.fail(checkDelivered, "no form delivers the resource to a tool that takes everything")
		}
		return
	}
	form := forms[a.form]
	for _, art := range a.artifacts {
		checkArtifact(r, res.Ref, form, caps, optA, art)
	}
	if len(a.artifacts) == 1 && a.artifacts[0].Name() != optA.Prefix+res.Ref.Code {
		r.fail(checkNames, "the one artifact is named %q, want the prefix followed by the code, %q", a.artifacts[0].Name(), optA.Prefix+res.Ref.Code)
	}
	var names []string
	for _, art := range a.artifacts {
		if slices.Contains(names, art.Name()) {
			r.fail(checkArtifacts, "two artifacts are named %q", art.Name())
		}
		names = append(names, art.Name())
	}

	// The same with another prefix: the names change by the prefix and nothing else.
	b := deliver(r, res.Spec, forms, caps, optB)
	if b.form != a.form || len(b.artifacts) != len(a.artifacts) {
		r.fatal(checkNames, "with the prefix %q form %d gave %d artifacts, with %q form %d gave %d; the prefix must not change what is delivered",
			optA.Prefix, a.form, len(a.artifacts), optB.Prefix, b.form, len(b.artifacts))
	}
	for i := range a.artifacts {
		na, nb := a.artifacts[i].Name(), b.artifacts[i].Name()
		if !strings.HasPrefix(nb, optB.Prefix) || strings.TrimPrefix(na, optA.Prefix) != strings.TrimPrefix(nb, optB.Prefix) {
			r.fail(checkNames, "artifact %d is %q with the prefix %q and %q with %q; the name is the prefix given followed by the same rest, and a kind never spells a prefix out",
				i, na, optA.Prefix, nb, optB.Prefix)
		}
	}
}

func checkArtifact(r reporter, ref resource.Ref, form kind.Form, caps adapter.Capabilities, opt kind.RenderOptions, art adapter.Artifact) {
	r.Helper()
	name := art.Name()
	if art.Ref() != ref {
		r.fail(checkNames, "artifact %q is for %v, want %v", name, art.Ref(), ref)
	}
	if !strings.HasPrefix(name, opt.Prefix) || len(name) == len(opt.Prefix) {
		r.fail(checkNames, "artifact %q does not carry the prefix %q followed by a name", name, opt.Prefix)
	}
	leaks := func(where, s string) {
		if strings.Contains(s, secretRef) {
			r.fail(checkSecrets, "artifact %q: %s holds %q as text; a reference to a secret travels as adapter.Value.Secret, so that the adapter writes what the tool offers for it", name, where, s)
		}
	}
	switch a := art.(type) {
	case adapter.SkillDir:
		if !caps.Skills {
			r.fail(checkArtifacts, "artifact %q is a skill directory for a tool that takes none", name)
		}
		if a.Degraded != form.Degraded {
			r.fail(checkArtifacts, "skill directory %q has Degraded %q and its form has %q; the explanation has to reach the plan", name, a.Degraded, form.Degraded)
		}
		var paths []string
		for _, f := range a.Files {
			if !fs.ValidPath(f.Path) || f.Path == "." || strings.Contains(f.Path, `\`) {
				r.fail(checkArtifacts, "skill directory %q: %q is not a relative path with forward slashes", name, f.Path)
			}
			if slices.Contains(paths, f.Path) {
				r.fail(checkArtifacts, "skill directory %q has %q twice", name, f.Path)
			}
			paths = append(paths, f.Path)
		}
		if !slices.Contains(paths, "SKILL.md") {
			r.fail(checkArtifacts, "skill directory %q has no SKILL.md: %v", name, paths)
		}
	case adapter.MCPServer:
		switch a.Transport {
		case adapter.Stdio:
			if !caps.MCP.Stdio {
				r.fail(checkArtifacts, "MCP server %q runs over stdio and the tool does not take that; the form should have declined", name)
			}
			if a.Command == "" || a.URL != "" || len(a.Headers) > 0 {
				r.fail(checkArtifacts, "stdio server %q needs a command and has no URL or headers: %+v", name, a)
			}
		case adapter.HTTP:
			if !caps.MCP.HTTP {
				r.fail(checkArtifacts, "MCP server %q is reached over HTTP and the tool does not take that; the form should have declined", name)
			}
			if a.URL == "" || a.Command != "" || len(a.Args) > 0 || len(a.Env) > 0 {
				r.fail(checkArtifacts, "HTTP server %q needs a URL and has no command, arguments or environment: %+v", name, a)
			}
		default:
			r.fail(checkArtifacts, "MCP server %q has the transport %q, which is neither stdio nor http", name, a.Transport)
		}
		leaks("the command", a.Command)
		leaks("the URL", a.URL)
		for _, arg := range a.Args {
			leaks("an argument", arg)
		}
		for what, values := range map[string]map[string]adapter.Value{"environment": a.Env, "header": a.Headers} {
			for key, v := range values {
				leaks(what+" "+key, v.Literal)
				if v.Secret != "" && !resource.ValidSecretName(v.Secret) {
					r.fail(checkArtifacts, "MCP server %q: %s %s refers to %q, which is not a secret name", name, what, key, v.Secret)
				}
			}
		}
	case adapter.Command:
		if !caps.Commands {
			r.fail(checkArtifacts, "artifact %q is a slash command for a tool that takes none", name)
		}
		if strings.TrimSpace(a.Body) == "" {
			r.fail(checkArtifacts, "slash command %q has no body", name)
		}
	case adapter.SubAgent:
		if !caps.Agents {
			r.fail(checkArtifacts, "artifact %q is a sub-agent for a tool that takes none", name)
		}
		if strings.TrimSpace(a.Body) == "" {
			r.fail(checkArtifacts, "sub-agent %q has no body", name)
		}
	default:
		r.fail(checkArtifacts, "artifact %q is a %T; artifacts are values of SkillDir, MCPServer, Command or SubAgent, never pointers", name, art)
	}
	if path.Base(name) != name {
		r.fail(checkNames, "artifact name %q contains a slash", name)
	}
}
