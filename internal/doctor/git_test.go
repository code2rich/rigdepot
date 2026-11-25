// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"path/filepath"
	"testing"

	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestGitWorks(t *testing.T) {
	env := newTestEnv(t)
	env.Git = testenv.NeedGit(t)
	r := only(t, gitCheck{}, env)
	expect(t, r, OK, "ok")
	if r.Facts["version"] == "" || r.Path == "" {
		t.Errorf("result: %+v", r)
	}
}

func TestGitMissing(t *testing.T) {
	env := newTestEnv(t)
	env.Git = filepath.Join(env.UserHome, "no-such-git")
	r := only(t, gitCheck{}, env)
	expect(t, r, Fail, "not_found")
	if r.Hint.IsZero() {
		t.Error("no hint")
	}
	// With a server, bundles come from there and git is not needed for syncing.
	env.Server = "https://hub.example.com"
	expect(t, only(t, gitCheck{}, env), Warn, "not_found")
}

// The git on PATH in tests is a stand-in that exits with an error: found, but broken.
func TestGitBroken(t *testing.T) {
	env := newTestEnv(t)
	r := only(t, gitCheck{}, env)
	expect(t, r, Fail, "broken")
	if r.Hint.IsZero() {
		t.Error("no hint")
	}
}
