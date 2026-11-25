// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package desired

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/gitrepo"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/repoformat"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.Run(m, func(env *testenv.Env) error {
		// Keep git away from the system and global configuration of the machine.
		if err := os.Setenv("GIT_CONFIG_NOSYSTEM", "1"); err != nil {
			return err
		}
		return os.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(env.Home, ".gitconfig"))
	}))
}

// skillKind stands in for builtin/kinds/skill, which the core must not import: skills/<code>/
// with a SKILL.md.
type skillKind struct{}

func (skillKind) ID() string          { return resource.KindSkill }
func (skillKind) Layout() kind.Layout { return kind.Layout{Dir: "skills"} }
func (skillKind) Schema() []byte      { return nil }
func (skillKind) Forms() []kind.Form  { return nil }
func (skillKind) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	if _, err := fs.Stat(files, "SKILL.md"); err != nil {
		return nil, errors.New("a skill needs a SKILL.md")
	}
	return ref.Code, nil
}

func kinds() *kind.Registry {
	r := kind.NewRegistry()
	r.Register(skillKind{})
	return r
}

// repoFiles is a resource repository with one skill per policy and scope that matters.
func repoFiles() map[string]string {
	files := map[string]string{
		brand.RepoConfig: "schema: 1\ndefaults:\n  policy: recommended\nproducts:\n  - { code: shop }\n  - { code: pay }\n",
	}
	skill := func(code, meta string) {
		files["skills/"+code+"/SKILL.md"] = "---\nname: " + code + "\ndescription: x\n---\n# " + code + "\n"
		files["skills/"+code+"/meta.yaml"] = "name: " + code + "\n" + meta
	}
	skill("required", "policy: required\n")
	skill("recommended", "")
	skill("optional", "policy: optional\n")
	skill("shop-only", "scope: shop\n")
	skill("pay-only", "scope: pay\n")
	skill("mine", "scope: me\n")
	skill("for-mgmt", "audience: mgmt\n")
	files["skills/broken/meta.yaml"] = "name: broken\n" // no SKILL.md
	return files
}

func mapFS(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for p, s := range files {
		m[p] = &fstest.MapFile{Data: []byte(s)}
	}
	return m
}

func codes[T any](items []T, ref func(T) resource.Ref) []string {
	var out []string
	for _, it := range items {
		out = append(out, ref(it).Code)
	}
	return out
}

func TestFromRepoFiltersByScopeAudiencePolicyAndSelection(t *testing.T) {
	repo, err := repoformat.Read(mapFS(repoFiles()), repoformat.Options{Kinds: kinds()})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		filter   Filter
		want     []string
		excluded map[string]Reason
	}{
		{
			name: "defaults",
			want: []string{"for-mgmt", "recommended", "required"},
			excluded: map[string]Reason{"mine": ExcludedPersonal, "optional": ExcludedNotEnabled,
				"pay-only": ExcludedProduct, "shop-only": ExcludedProduct},
		},
		{
			name:   "a product, an audience, a selection",
			filter: Filter{Products: []string{"shop"}, Audiences: []resource.Audience{resource.AudienceRD}, Selection: Selection{Enabled: []string{"optional"}, Disabled: []string{"recommended", "required"}}},
			// required stays although the selection lists it as disabled
			want: []string{"optional", "required", "shop-only"},
			excluded: map[string]Reason{"for-mgmt": ExcludedAudience, "mine": ExcludedPersonal, "pay-only": ExcludedProduct,
				"recommended": ExcludedDisabled},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := FromRepo(repo, Source{Type: SourceRepo, URL: "https://git.example.com/r.git"}, c.filter)
			if got := codes(st.Resources, func(r Resource) resource.Ref { return r.Ref }); !slices.Equal(got, c.want) {
				t.Errorf("delivered %v, want %v", got, c.want)
			}
			got := map[string]Reason{}
			for _, e := range st.Excluded {
				got[e.Ref.Code] = e.Reason
			}
			if len(got) != len(c.excluded) {
				t.Errorf("excluded %v, want %v", got, c.excluded)
			}
			for code, why := range c.excluded {
				if got[code] != why {
					t.Errorf("%s excluded as %q, want %q", code, got[code], why)
				}
			}
			// A resource that cannot be read is unknown, never just absent.
			if len(st.Failed) != 1 || st.Failed[0].Ref.Code != "broken" || !st.Known("broken") {
				t.Errorf("failed = %+v", st.Failed)
			}
			if st.Known("never-there") {
				t.Error("a code the repository does not hold is known")
			}
			r, ok := st.Lookup("required")
			if !ok || r.Kind == nil || r.Spec != "required" || !r.Digest.Valid() || r.Meta.Policy != resource.PolicyRequired {
				t.Errorf("Lookup(required) = %+v, %v", r, ok)
			}
		})
	}
}

func TestSelectionFile(t *testing.T) {
	dir, err := home.At(filepath.Join(t.TempDir(), brand.Home))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ReadSelection(dir)
	if err != nil || len(s.Enabled)+len(s.Disabled) != 0 {
		t.Fatalf("a missing selection reads as %+v, %v; want empty", s, err)
	}

	if !s.Enable("optional", resource.PolicyOptional) || s.Enable("optional", resource.PolicyOptional) {
		t.Error("enabling an optional resource changes the selection once")
	}
	if s.Enable("required", resource.PolicyRequired) {
		t.Error("enabling a required resource changed the selection")
	}
	if ch, err := s.Disable("recommended", resource.PolicyRecommended); !ch || err != nil {
		t.Errorf("Disable(recommended) = %v, %v", ch, err)
	}
	if _, err := s.Disable("required", resource.PolicyRequired); !errors.Is(err, ErrRequired) {
		t.Errorf("Disable(required) = %v, want ErrRequired", err)
	}
	if err := WriteSelection(dir, s); err != nil {
		t.Fatal(err)
	}
	back, err := ReadSelection(dir)
	if err != nil || !slices.Equal(back.Enabled, []string{"optional"}) || !slices.Equal(back.Disabled, []string{"recommended"}) {
		t.Fatalf("read back %+v, %v", back, err)
	}
	if !back.Enable("recommended", resource.PolicyRecommended) || len(back.Disabled) != 0 {
		t.Errorf("enabling a disabled recommended resource left %+v", back)
	}
	if ch, _ := back.Disable("optional", resource.PolicyOptional); !ch || len(back.Enabled) != 0 {
		t.Errorf("disabling an enabled optional resource left %+v", back)
	}

	// Not being able to read it is not knowing, never an empty selection.
	p := dir.Join(SelectionFile)
	for name, content := range map[string]string{
		"corrupt": "{\"schema\": 1, \"enabled\": [",
		"newer":   "{\"schema\": 2, \"enabled\": []}",
		"wrong":   "{\"schema\": 1, \"enabled\": \"optional\"}",
	} {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSelection(dir); !errors.Is(err, ErrSelectionUnknown) {
			t.Errorf("%s: ReadSelection = %v, want ErrSelectionUnknown", name, err)
		}
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o700); err != nil { // there, and not a file that can be read
		t.Fatal(err)
	}
	if _, err := ReadSelection(dir); !errors.Is(err, ErrSelectionUnknown) {
		t.Errorf("unreadable: ReadSelection = %v, want ErrSelectionUnknown", err)
	}
}

// git runs the real git in dir.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(testenv.NeedGit(t), append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=dev@example.com",
		"GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=dev@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// The whole way from a Git repository: archive, tree, repository format, filter. The work tree
// is committed with CRLF line endings in one file; the digest is of the stored bytes, so it is
// the one the repository format computes for the same bytes in memory.
func TestReadRepoFromGit(t *testing.T) {
	work := t.TempDir()
	files := repoFiles()
	files["skills/recommended/SKILL.md"] = strings.ReplaceAll(files["skills/recommended/SKILL.md"], "\n", "\r\n")
	for p, s := range files {
		full := filepath.Join(work, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, work, "init", "--quiet", "--initial-branch=main")
	git(t, work, "add", "--all")
	git(t, work, "-c", "core.autocrlf=false", "commit", "--quiet", "--message", "resources")

	f, err := gitrepo.New(gitrepo.Options{Git: testenv.NeedGit(t), CacheDir: filepath.Join(t.TempDir(), "cache"), AllowLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	repo, src, err := ReadRepo(context.Background(), f, gitrepo.Source{URL: work}, repoformat.Options{Kinds: kinds()})
	if err != nil {
		t.Fatal(err)
	}
	if src.Type != SourceRepo || src.URL != work || len(src.Commit) != 40 {
		t.Errorf("source = %+v", src)
	}
	inMemory, err := repoformat.Read(mapFS(files), repoformat.Options{Kinds: kinds()})
	if err != nil {
		t.Fatal(err)
	}
	st := FromRepo(repo, src, Filter{})
	want := FromRepo(inMemory, src, Filter{})
	if len(st.Resources) != len(want.Resources) || len(st.Resources) == 0 {
		t.Fatalf("from git %d resources, from memory %d", len(st.Resources), len(want.Resources))
	}
	for i := range st.Resources {
		if st.Resources[i].Ref != want.Resources[i].Ref || st.Resources[i].Digest != want.Resources[i].Digest {
			t.Errorf("%s: digest from git %s, from the same bytes in memory %s", st.Resources[i].Ref, st.Resources[i].Digest, want.Resources[i].Digest)
		}
	}

	// A repository that cannot be had is an error, never an empty desired state.
	if _, _, err := ReadRepo(context.Background(), f, gitrepo.Source{URL: filepath.Join(t.TempDir(), "missing")}, repoformat.Options{Kinds: kinds()}); err == nil {
		t.Error("reading a repository that does not exist succeeded")
	}
}

func TestTheSourceKeepsNoPassword(t *testing.T) {
	for in, want := range map[string]string{
		"https://dev:secret-value@git.example.com/r.git": "https://dev@git.example.com/r.git",
		"https://git.example.com/r.git":                  "https://git.example.com/r.git",
		"git@git.example.com:team/r.git":                 "git@git.example.com:team/r.git",
	} {
		if got := withoutPassword(in); got != want {
			t.Errorf("withoutPassword(%q) = %q, want %q", in, got, want)
		}
	}
}
