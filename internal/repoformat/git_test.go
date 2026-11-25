// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package repoformat

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/bundle"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// gitRun runs the real git in dir, with nothing from the machine's or the user's Git
// configuration (the home directory is the temporary one already).
func gitRun(t *testing.T, git, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(git, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=author@example.com",
		"GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=author@example.com",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return out
}

// archiveArgs is how the content of a commit has to be taken out of a repository so that it
// does not depend on the line-ending settings of whoever runs it: git archive applies
// core.autocrlf and core.eol, and the -c options switch both off for this one command.
// internal/gitrepo runs git archive the same way.
var archiveArgs = []string{"-c", "core.autocrlf=false", "-c", "core.eol=lf", "archive", "--format=tar", "HEAD"}

func digests(t *testing.T, repo *Repo) map[string]resource.Digest {
	t.Helper()
	if len(repo.Failed) > 0 {
		t.Fatalf("failed: %+v", repo.Failed)
	}
	out := map[string]resource.Digest{}
	for _, r := range repo.Resources {
		out[r.Ref.Code] = r.Digest
	}
	return out
}

// The same content gives the same digest whether a person's checkout has LF or CRLF line
// endings, because the content is read from git archive and not from the work tree.
func TestDigestIsTheSameForLFAndCRLFCheckouts(t *testing.T) {
	git := testenv.NeedGit(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")

	// A repository written with LF, as an author on macOS or Linux commits it. text=auto
	// makes line-ending conversion apply on every machine, not only where autocrlf is set.
	if err := os.CopyFS(src, os.DirFS("testdata/repo")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".gitattributes"), []byte("* text=auto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, git, src, "init", "-q", "-b", "main")
	gitRun(t, git, src, "-c", "core.autocrlf=false", "add", "-A")
	gitRun(t, git, src, "-c", "core.autocrlf=false", "commit", "-q", "-m", "resources")

	// Two people: one with the Windows default core.autocrlf=true, one without.
	crlf := filepath.Join(root, "crlf")
	lf := filepath.Join(root, "lf")
	gitRun(t, git, root, "-c", "core.autocrlf=true", "clone", "-q", src, crlf)
	gitRun(t, git, crlf, "config", "core.autocrlf", "true")
	gitRun(t, git, crlf, "config", "core.eol", "crlf")
	gitRun(t, git, root, "-c", "core.autocrlf=false", "clone", "-q", src, lf)

	skill, err := os.ReadFile(filepath.Join(crlf, "skills", "unit-testing", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(skill, []byte("\r\n")) {
		t.Fatal("the checkout with core.autocrlf=true has no CRLF; the test would show nothing")
	}

	// Reading the work trees: the digests differ. This is the problem.
	fromLF := digests(t, read(t, os.DirFS(lf)))
	fromCRLF := digests(t, read(t, os.DirFS(crlf)))
	if fromLF["unit-testing"] == fromCRLF["unit-testing"] {
		t.Fatal("the LF and CRLF work trees have the same digest; the test would show nothing")
	}

	// Reading git archive: the same, and the same as the content as committed.
	archived := func(dir string) map[string]resource.Digest {
		tree, err := bundle.ReadTree(bytes.NewReader(gitRun(t, git, dir, archiveArgs...)))
		if err != nil {
			t.Fatal(err)
		}
		return digests(t, read(t, tree))
	}
	a, b := archived(lf), archived(crlf)
	if len(a) != 4 {
		t.Fatalf("read %d resources from the archive, want 4", len(a))
	}
	for code, d := range a {
		if b[code] != d {
			t.Errorf("%s: %s from the LF checkout, %s from the CRLF one", code, d, b[code])
		}
		if fromLF[code] != d {
			t.Errorf("%s: %s from the archive, %s from the LF work tree", code, d, fromLF[code])
		}
	}

	// Without the -c options, git archive in the CRLF checkout converts line endings too;
	// that is why internal/gitrepo has to pass them. Logged, not asserted: it is git's
	// behaviour, not ours.
	plain, err := bundle.ReadTree(bytes.NewReader(gitRun(t, git, crlf, "archive", "--format=tar", "HEAD")))
	if err != nil {
		t.Fatal(err)
	}
	if digests(t, read(t, plain))["unit-testing"] != a["unit-testing"] {
		t.Log("plain git archive under core.autocrlf=true converted line endings; the -c options are needed")
	}
}
