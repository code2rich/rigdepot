// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package repoformat

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/bundle"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func read(t *testing.T, fsys fs.FS) *Repo {
	t.Helper()
	repo, err := Read(fsys, Options{Kinds: testKinds(), Version: "0.4.0"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return repo
}

func codes(issues []Issue) []string {
	var out []string
	for _, is := range issues {
		out = append(out, is.File+" "+is.Code)
	}
	return out
}

func TestReadExampleRepository(t *testing.T) {
	fsys := os.DirFS("testdata/repo")
	repo := read(t, fsys)

	if len(repo.Failed) > 0 {
		t.Fatalf("resources failed: %+v", repo.Failed)
	}
	var got []string
	for _, r := range repo.Resources {
		got = append(got, r.Ref.String()+" "+r.Path)
	}
	want := []string{
		"mcp/gitlab mcp/gitlab.yaml",
		"mcp/jira mcp/jira.yaml",
		"skill/release-notes skills/release-notes",
		"skill/unit-testing skills/unit-testing",
	}
	if !slices.Equal(got, want) {
		t.Errorf("resources:\n got %q\nwant %q", got, want)
	}

	cfg := repo.Config
	if cfg.Schema != 1 || cfg.Locale != "zh-CN" || len(cfg.Stages) != 5 || len(cfg.Products) != 2 || cfg.Secrets["JIRA_API_TOKEN"].Label == "" {
		t.Errorf("config = %+v", cfg)
	}
	if s, ok := cfg.Stage("scm"); !ok || !s.Hidden {
		t.Errorf(`Stage("scm") = %+v, %v`, s, ok)
	}

	// The advisories: a directory no kind owns, a file in a kind's directory that is not a
	// resource, a misspelt field, a scope that is not a product. Fields starting with x- are
	// the author's own and not reported; dot files are not reported.
	wantNotices := []string{
		"docs unknown-dir",
		"mcp/README.md ignored",
		"mcp/gitlab.yaml unknown-product",
		"skills/release-notes/meta.yaml unknown-field",
	}
	if got := codes(repo.Notices); !slices.Equal(got, wantNotices) {
		t.Errorf("notices:\n got %q\nwant %q\n%v", got, wantNotices, repo.Notices)
	}
	for _, n := range repo.Notices {
		if n.Code == CodeUnknownField && (n.Field != "audiance" || n.Line != 5) {
			t.Errorf("the misspelt field is reported as %+v; want field audiance on line 5", n)
		}
	}

	// The digest is of the resource's files, and a single-file resource is a directory with
	// one file.
	ut, _ := repo.Lookup("unit-testing")
	sub, _ := fs.Sub(fsys, "skills/unit-testing")
	if d, err := bundle.Digest(sub); err != nil || d != ut.Digest {
		t.Errorf("unit-testing digest %s, digest of its directory %s (%v)", ut.Digest, d, err)
	}
	jira, _ := repo.Lookup("jira")
	data, _ := fs.ReadFile(fsys, "mcp/jira.yaml")
	one := fstest.MapFS{"jira.yaml": {Data: data}}
	if d, _ := bundle.Digest(one); d != jira.Digest {
		t.Errorf("jira digest %s, digest of a directory holding only jira.yaml %s", jira.Digest, d)
	}
	if paths := jira.Files.(*bundle.Files).Paths(); !slices.Equal(paths, []string{"jira.yaml"}) {
		t.Errorf("jira files = %v", paths)
	}

	// Metadata as written, and with the repository's defaults.
	if ut.Meta.Name != "单元测试" || ut.Meta.Policy != "" || ut.Meta.I18n["en"].Name != "Unit testing" {
		t.Errorf("unit-testing meta = %+v", ut.Meta)
	}
	if e := ut.Effective; e.Scope != resource.ScopeOrg || e.Policy != resource.PolicyRecommended || e.Audience != resource.AudienceRD {
		t.Errorf("unit-testing effective meta = %+v", e)
	}
	rn, _ := repo.Lookup("release-notes")
	if e := rn.Effective; e.Scope != "shop" || e.Policy != resource.PolicyOptional {
		t.Errorf("a resource's own scope and policy are replaced by the defaults: %+v", e)
	}
	if jira.Meta.Policy != resource.PolicyRequired || jira.Meta.Name != "Jira" {
		t.Errorf("jira meta from the meta key = %+v", jira.Meta)
	}
	if jira.Kind.ID() != resource.KindMCP || jira.Spec == nil {
		t.Errorf("jira kind %v, spec %v", jira.Kind, jira.Spec)
	}
}

// One map per broken resource keeps each case next to what it has to produce.
func brokenRepo() fstest.MapFS {
	f := fstest.MapFS{
		brand.RepoConfig: {Data: []byte("schema: 1\nsecrets:\n  JIRA_API_TOKEN: {label: Jira}\n")},
		// Still read: a problem elsewhere does not stop it.
		"skills/good/SKILL.md":  {Data: []byte("---\nname: good\ndescription: fine\n---\n")},
		"skills/good/meta.yaml": {Data: []byte("name: Good\nstages: [develop]\n")},
	}
	add := func(p, body string) { f[p] = &fstest.MapFile{Data: []byte(body)} }
	add("skills/Bad_Name/SKILL.md", "---\nname: Bad_Name\n---\n")
	add("skills/Bad_Name/meta.yaml", "name: x\n")
	add("skills/no-meta/SKILL.md", "---\nname: no-meta\n---\n")
	add("skills/bad-policy/SKILL.md", "---\nname: bad-policy\n---\n")
	add("skills/bad-policy/meta.yaml", "name: Bad policy\n\npolicy: mandatory\n")
	add("skills/no-stage/SKILL.md", "---\nname: no-stage\n---\n")
	add("skills/no-stage/meta.yaml", "name: x\nstages: [develop, perf]\n")
	add("skills/no-secret/SKILL.md", "---\nname: no-secret\n---\n")
	add("skills/no-secret/meta.yaml", "name: x\nsecrets: [JIRA_API_TOKEN, GITLAB_TOKEN]\n")
	add("skills/wrong-name/SKILL.md", "---\nname: something-else\n---\n")
	add("skills/wrong-name/meta.yaml", "name: x\n")
	add("skills/twice/SKILL.md", "---\nname: twice\n---\n")
	add("skills/twice/meta.yaml", "name: x\nname: y\n")
	add("skills/jira/SKILL.md", "---\nname: jira\n---\n")
	add("skills/jira/meta.yaml", "name: Jira helper\n")
	add("mcp/jira.yaml", "meta: {name: Jira}\nserver: {type: http, url: https://jira.example.com/mcp}\n")
	add("mcp/no-server.yaml", "meta: {name: x}\n")
	add("mcp/no-meta.yaml", "server: {type: http, url: https://x.example.com/mcp}\n")
	add("mcp/bad-type.yaml", "meta: {name: x}\nserver:\n  type: websocket\n")
	add("mcp/no-command.yaml", "meta: {name: x}\nserver: {type: stdio}\n")
	return f
}

func TestReadFailsOnlyTheBrokenResources(t *testing.T) {
	repo := read(t, brokenRepo())

	if len(repo.Resources) != 1 || repo.Resources[0].Ref.Code != "good" {
		t.Errorf("read %v; only good can be used", repo.Resources)
	}
	want := map[string][]string{
		"mcp/bad-type.yaml":   {"mcp/bad-type.yaml schema"},
		"mcp/jira.yaml":       {"mcp/jira.yaml duplicate-code"},
		"mcp/no-command.yaml": {"mcp/no-command.yaml parse"},
		"mcp/no-meta.yaml":    {"mcp/no-meta.yaml missing-meta"},
		"mcp/no-server.yaml":  {"mcp/no-server.yaml schema"},
		"skills/Bad_Name":     {"skills/Bad_Name invalid-code"},
		"skills/bad-policy":   {"skills/bad-policy/meta.yaml schema"},
		"skills/jira":         {"skills/jira duplicate-code"},
		"skills/no-meta":      {"skills/no-meta/meta.yaml missing-meta"},
		"skills/no-secret":    {"skills/no-secret/meta.yaml undefined-secret"},
		"skills/no-stage":     {"skills/no-stage/meta.yaml undefined-stage"},
		"skills/twice":        {"skills/twice/meta.yaml yaml"},
		"skills/wrong-name":   {"skills/wrong-name parse"},
	}
	got := map[string][]string{}
	for _, f := range repo.Failed {
		got[f.Path] = codes(f.Issues)
	}
	for p, w := range want {
		if !slices.Equal(got[p], w) {
			t.Errorf("%s failed with %q, want %q", p, got[p], w)
		}
	}
	for p := range got {
		if want[p] == nil {
			t.Errorf("%s failed and should not have: %q", p, got[p])
		}
	}

	// Errors point at the line and the field.
	for _, f := range repo.Failed {
		is := f.Issues[0]
		switch f.Path {
		case "skills/bad-policy":
			if is.Line != 3 || is.Field != "policy" {
				t.Errorf("bad-policy: %+v, want line 3, field policy", is)
			}
		case "skills/no-stage":
			if is.Line != 2 || is.Field != "stages.1" || !strings.Contains(is.Message, "perf") {
				t.Errorf("no-stage: %+v", is)
			}
		case "skills/no-secret":
			if len(f.Issues) != 1 || is.Field != "secrets.1" {
				t.Errorf("no-secret: %+v", f.Issues)
			}
		case "skills/twice":
			if is.Line != 2 {
				t.Errorf("twice: %+v, want line 2", is)
			}
		case "mcp/bad-type.yaml":
			if is.Line != 3 || is.Field != "server.type" {
				t.Errorf("bad-type: %+v, want line 3, field server.type", is)
			}
		}
	}
}

func TestReadRefusesTheRepositoryWhenTheConfigCannotBeUsed(t *testing.T) {
	good := brokenRepo()
	for name, c := range map[string]struct {
		config  string
		version string
		check   func(error) bool
	}{
		"newer format": {config: "schema: 2\nsomething: new\n", check: func(err error) bool {
			var e *NewerFormatError
			return errors.Is(err, ErrNewerFormat) && errors.As(err, &e) && e.Schema == 2
		}},
		"newer program": {config: "schema: 1\nrequires: {rgd: \">=9.0.0\"}\n", version: "0.4.0", check: func(err error) bool {
			return errors.Is(err, ErrNeedsNewer)
		}},
		"invalid": {config: "schema: 1\nstages:\n  - { id: Develop }\n", check: func(err error) bool {
			var e *ConfigError
			return errors.As(err, &e) && e.Issues[0].Line == 3 && e.Issues[0].Field == "stages.0.id"
		}},
		"no schema": {config: "name: x\n", check: func(err error) bool {
			var e *ConfigError
			return errors.As(err, &e)
		}},
		"not yaml": {config: "schema: 1\nstages: [\n", check: func(err error) bool {
			var e *ConfigError
			return errors.As(err, &e) && e.Issues[0].Code == CodeYAML
		}},
		"stage twice": {config: "schema: 1\nstages: [{id: develop}, {id: test}, {id: develop}]\n", check: func(err error) bool {
			var e *ConfigError
			return errors.As(err, &e) && e.Issues[0].Code == CodeDuplicate && e.Issues[0].Field == "stages.2.id"
		}},
	} {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for k, v := range good {
				fsys[k] = v
			}
			fsys[brand.RepoConfig] = &fstest.MapFile{Data: []byte(c.config)}
			repo, err := Read(fsys, Options{Kinds: testKinds(), Version: c.version})
			if repo != nil || !c.check(err) {
				t.Errorf("Read = %v, %v", repo, err)
			}
		})
	}

	if _, err := Read(fstest.MapFS{"skills/x/SKILL.md": {}}, Options{Kinds: testKinds()}); !errors.Is(err, ErrNoConfig) {
		t.Errorf("a directory without %s: %v", brand.RepoConfig, err)
	}
	// A development build is never too old: it may be ahead of every release.
	good[brand.RepoConfig] = &fstest.MapFile{Data: []byte("schema: 1\nrequires: {rgd: \">=9.0.0\"}\n")}
	for _, v := range []string{"", "0.0.0-dev+0123abc", "9.0.0", "10.0.0"} {
		if _, err := Read(good, Options{Kinds: testKinds(), Version: v}); err != nil {
			t.Errorf("version %q: %v", v, err)
		}
	}
}

// Without any kind registered (the built-in kinds are not linked in), every resource directory
// is unknown and nothing is delivered, but the repository still reads.
func TestReadWithoutKinds(t *testing.T) {
	repo, err := Read(os.DirFS("testdata/repo"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.Resources) != 0 || len(repo.Failed) != 0 {
		t.Errorf("read %v, failed %v", repo.Resources, repo.Failed)
	}
	var dirs []string
	for _, n := range repo.Notices {
		if n.Code == CodeUnknownDir {
			dirs = append(dirs, n.File)
		}
	}
	if !slices.Equal(dirs, []string{"docs", "mcp", "skills"}) {
		t.Errorf("unknown directories = %v", dirs)
	}
}

// Every row of the path safety table, reached through a repository archive: the resource that
// breaks the rule is refused, the one next to it is read.
func TestReadRefusesUnsafeResources(t *testing.T) {
	type entry struct {
		name string
		typ  byte
		size int
	}
	cases := []struct {
		rule    bundle.Rule
		entries []entry
	}{
		{bundle.RuleNotRelative, []entry{{name: `skills/bad/refs\x.md`}}},
		{bundle.RuleLink, []entry{{name: "skills/bad/link.md", typ: tar.TypeSymlink}}},
		{bundle.RuleLink, []entry{{name: "skills/bad/hard.md", typ: tar.TypeLink}}},
		{bundle.RuleSpecial, []entry{{name: "skills/bad/pipe", typ: tar.TypeFifo}}},
		{bundle.RuleWindowsName, []entry{{name: "skills/bad/nul.md"}}},
		{bundle.RuleWindowsName, []entry{{name: "skills/bad/a|b.md"}}},
		{bundle.RuleCaseCollision, []entry{{name: "skills/bad/Notes.md"}, {name: "skills/bad/notes.md"}}},
		{bundle.RuleTooLong, []entry{{name: "skills/bad/" + strings.Repeat("x", bundle.MaxPathBytes) + ".md"}}},
		{bundle.RuleTooDeep, []entry{{name: "skills/bad/1/2/3/4/5/6/7/8/9.md"}}},
		{bundle.RuleTooManyFiles, func() []entry {
			var out []entry
			for i := range bundle.MaxFiles {
				out = append(out, entry{name: fmt.Sprintf("skills/bad/refs/%03d.md", i)})
			}
			return out
		}()},
		{bundle.RuleTooLarge, []entry{{name: "skills/bad/big.bin", size: bundle.MaxBytes}}},
		// A file-shaped resource whose name Windows reserves.
		{bundle.RuleWindowsName, []entry{{name: "mcp/con.yaml"}}},
		// The resource directory itself is a link.
		{bundle.RuleLink, []entry{{name: "skills/bad", typ: tar.TypeSymlink}}},
	}
	for _, c := range cases {
		t.Run(string(c.rule)+" "+c.entries[0].name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			write := func(name string, typ byte, body []byte) {
				h := &tar.Header{Name: name, Typeflag: typ, Mode: 0o644, Size: int64(len(body)), Format: tar.FormatPAX}
				if typ != tar.TypeReg {
					h.Size, h.Linkname = 0, "SKILL.md"
				}
				if err := tw.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
				if typ == tar.TypeReg {
					tw.Write(body)
				}
			}
			write(brand.RepoConfig, tar.TypeReg, []byte("schema: 1\n"))
			write("skills/good/SKILL.md", tar.TypeReg, []byte("---\nname: good\n---\n"))
			write("skills/good/meta.yaml", tar.TypeReg, []byte("name: Good\n"))
			if !strings.HasPrefix(c.entries[0].name, "mcp/") && c.entries[0].typ != tar.TypeSymlink || len(c.entries) > 1 {
				write("skills/bad/SKILL.md", tar.TypeReg, []byte("---\nname: bad\n---\n"))
				write("skills/bad/meta.yaml", tar.TypeReg, []byte("name: Bad\n"))
			}
			for _, e := range c.entries {
				typ := e.typ
				if typ == 0 {
					typ = tar.TypeReg
				}
				body := []byte("meta: {name: x}\nserver: {type: http}\n")
				if e.size > 0 {
					body = make([]byte, e.size)
				}
				write(e.name, typ, body)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			tree, err := bundle.ReadTree(&buf)
			if err != nil {
				t.Fatal(err)
			}
			repo := read(t, tree)
			if len(repo.Resources) != 1 || repo.Resources[0].Ref.Code != "good" {
				t.Errorf("read %v; only good can be used", repo.Resources)
			}
			if len(repo.Failed) != 1 {
				t.Fatalf("failed: %+v", repo.Failed)
			}
			found := false
			for _, is := range repo.Failed[0].Issues {
				found = found || (is.Code == CodeUnsafePath && is.Field == string(c.rule))
			}
			if !found {
				t.Errorf("%s failed with %v, want %s", repo.Failed[0].Path, repo.Failed[0].Issues, c.rule)
			}
		})
	}
}

// A resource on disk that is a symbolic link to a directory is not read through.
func TestReadDoesNotFollowALinkedResourceDirectory(t *testing.T) {
	if !symlinksWork(t) {
		t.Skip("this file system does not create symbolic links for an unprivileged user")
	}
	dir := t.TempDir()
	write := func(p, body string) {
		full := path.Join(dir, p)
		if err := os.MkdirAll(path.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(brand.RepoConfig, "schema: 1\n")
	write("elsewhere/SKILL.md", "---\nname: linked\n---\n")
	write("elsewhere/meta.yaml", "name: x\n")
	if err := os.MkdirAll(path.Join(dir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../elsewhere", path.Join(dir, "skills", "linked")); err != nil {
		t.Fatal(err)
	}
	repo := read(t, os.DirFS(dir))
	if len(repo.Resources) != 0 || len(repo.Failed) != 1 || repo.Failed[0].Issues[0].Field != string(bundle.RuleLink) {
		t.Errorf("read %v, failed %+v", repo.Resources, repo.Failed)
	}
}

func symlinksWork(t *testing.T) bool {
	dir := t.TempDir()
	return os.Symlink("x", path.Join(dir, "y")) == nil
}

func FuzzReadConfigAndMeta(f *testing.F) {
	for _, s := range []string{
		"schema: 1\n",
		"schema: 1\nstages: [{id: develop}]\ndefaults: {scope: shop}\nproducts: [{code: shop}]\n",
		"schema: 2\n",
		"schema: 1\nrequires: {rgd: \">=1.2.3\"}\n",
		"a: &x 1\nb: *x\n",
		"- 1\n- 2\n",
		"",
	} {
		f.Add([]byte(s), []byte("name: x\nstages: [develop]\n"))
	}
	f.Fuzz(func(t *testing.T, config, meta []byte) {
		fsys := fstest.MapFS{
			brand.RepoConfig:     {Data: config},
			"skills/x/meta.yaml": {Data: meta},
			"skills/x/SKILL.md":  {Data: []byte("---\nname: x\n---\n")},
			"mcp/xy.yaml":        {Data: meta},
		}
		repo, err := Read(fsys, Options{Kinds: testKinds()})
		if err != nil {
			return
		}
		// Every resource is either read or failed, never both, never neither.
		if n := len(repo.Resources) + len(repo.Failed); n != 2 {
			t.Fatalf("%d resources accounted for, want 2: %+v %+v", n, repo.Resources, repo.Failed)
		}
	})
}
