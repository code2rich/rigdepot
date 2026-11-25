// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/gitrepo"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// localGit is the person's git, with the address of the example's knowledge base pointing at a
// repository on this disk: the test never reaches a network.
type localGit struct {
	f     *gitrepo.Fetcher
	repos map[string]string // address -> local path
	asked []string
}

func (g *localGit) Snapshot(ctx context.Context, src gitrepo.Source) (*gitrepo.Snapshot, error) {
	g.asked = append(g.asked, src.URL)
	src.URL = g.repos[src.URL]
	return g.f.Snapshot(ctx, src)
}

// The knowledge base of the example repository: the repository holds a pointer, sync fetches the
// content with the person's own git into the local data directory, and the AI gets a skill that
// says where it is. A fetch that fails keeps the copy from before and leaves the exit code at 3.
func TestAKnowledgeBaseIsFetchedWithThePersonsOwnGit(t *testing.T) {
	w := newWorld(t)
	git := testenv.NeedGit(t)
	handbook := filepath.Join(t.TempDir(), "handbook")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", handbook, "-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for p, content := range map[string]string{
		"rules/backend/naming.md":    "# 命名\n\n接口路径用小写和短横线。\n",
		"rules/backend/db/tables.md": "# 表\n\n每张表都有 created_at。\n",
		"rules/frontend/style.md":    "not part of it\n",
	} {
		full := filepath.Join(handbook, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "--quiet", "--initial-branch=main")
	run("add", "--all")
	run("commit", "--quiet", "--message", "handbook")
	f, err := gitrepo.New(gitrepo.Options{Git: git, CacheDir: filepath.Join(t.TempDir(), "cache"), AllowLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	const address = "https://gitlab.example.com/platform/handbook.git"
	lg := &localGit{f: f, repos: map[string]string{address: handbook}}
	w.knowledge = lg

	w.must(cliout.ExitOK, "enable", "eng-handbook", "--repo", w.repo)
	r := w.sync(cliout.ExitOK, "--plan")
	inOrder(t, r.stdout, "Knowledge bases", "~/"+brand.Home+"/knowledge/eng-handbook/", "from "+address)
	if len(lg.asked) > 0 {
		t.Error("--plan fetched a knowledge base")
	}

	r = w.sync(cliout.ExitOK, "--yes-global")
	inOrder(t, r.stdout, "Knowledge bases", "knowledge/eng-handbook/", "2 files")
	content := "~/" + brand.Home + "/knowledge/eng-handbook/"
	if got := w.read(content + "naming.md"); !strings.Contains(got, "接口路径") {
		t.Errorf("naming.md: %q", got)
	}
	if w.exists(content+"style.md") || !w.exists(content+"db/tables.md") {
		t.Error("the content is not the directory the pointer names")
	}
	skill := w.read("~/.fakecode/skills/" + brand.Prefix + "eng-handbook/SKILL.md")
	if !strings.Contains(skill, content) || strings.Contains(skill, "接口路径") {
		t.Errorf("the skill does not point at the content, or holds it:\n%s", skill)
	}

	// The repository cannot be reached: the copy stays, and the person hears of it.
	lg.repos[address] = filepath.Join(t.TempDir(), "gone")
	r = w.sync(cliout.ExitPending, "--yes-global")
	inOrder(t, r.stdout, "knowledge/eng-handbook/", "could not be fetched")
	if !w.exists(content + "naming.md") {
		t.Error("a failed fetch removed the copy from before")
	}

	// Turned off: the content goes with the skill.
	lg.repos[address] = handbook
	w.must(cliout.ExitOK, "disable", "eng-handbook", "--repo", w.repo)
	w.sync(cliout.ExitOK, "--yes-global")
	if w.exists(content) {
		t.Error("the content of a knowledge base that was turned off is still there")
	}
}
