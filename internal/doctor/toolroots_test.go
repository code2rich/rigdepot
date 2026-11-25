// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("creating symbolic links needs a privilege on Windows; the walk is the same code on every platform")
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func rootsEnv(t *testing.T, roots map[string]string) *Env {
	env := newTestEnv(t)
	env.Adapters = []adapter.Adapter{&fakeAdapter{id: "fake", verified: "1.0",
		detection: adapter.Detection{Status: adapter.Installed, Roots: roots}}}
	return env
}

// The dotfiles layout: ~/.fake is a link into a directory outside the home directory.
func TestToolRootLinkLeavesHome(t *testing.T) {
	env := rootsEnv(t, map[string]string{"config": "~/.fake", "skills": "~/.fake/skills"})
	outside := filepath.Join(testenv.Current(t).Home, "dotfiles-"+t.Name(), "fake")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink(t, outside, filepath.Join(env.UserHome, ".fake"))
	r := only(t, toolRootsCheck{}, env)
	expect(t, r, Fail, "link_outside_home")
	if r.Subject != "fake" || r.Path != filepath.Join(env.UserHome, ".fake") || r.Hint.IsZero() {
		t.Errorf("result: %+v", r)
	}
}

// A link that stays inside the home directory, a root that does not exist and a root moved out
// of the home directory by an environment variable are all writable.
func TestToolRootsWritable(t *testing.T) {
	env := rootsEnv(t, map[string]string{"config": "~/.fake", "data": "~/.local/share/fake", "other": "/opt/fake"})
	inside := filepath.Join(env.UserHome, "dotfiles", "fake")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink(t, inside, filepath.Join(env.UserHome, ".fake"))
	r := only(t, toolRootsCheck{}, env)
	expect(t, r, OK, "ok")
}

func TestToolRootsNoTool(t *testing.T) {
	env := newTestEnv(t)
	env.Adapters = []adapter.Adapter{&fakeAdapter{id: "fake", detection: adapter.Detection{Status: adapter.NotInstalled}}}
	expect(t, only(t, toolRootsCheck{}, env), Skipped, "no_tools")
}
