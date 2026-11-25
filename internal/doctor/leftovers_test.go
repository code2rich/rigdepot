// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"os"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/home"
)

func TestLeftoversNone(t *testing.T) {
	env := newTestEnv(t)
	expect(t, only(t, leftoversCheck{}, env), OK, "none")
	mkdata(t, env)
	writeFile(t, env.Home.Join(home.ManifestFile), "{}")
	expect(t, only(t, leftoversCheck{}, env), OK, "none")
}

func TestLeftoversFound(t *testing.T) {
	env := newTestEnv(t)
	old := time.Now().Add(-3 * time.Hour)
	for _, p := range []string{
		env.Home.Join(home.TmpDir, "plan-123/home/.claude.json"),
		env.Home.Join("." + home.ManifestFile + ".tmp-456"),
		env.Home.Join(home.StateDir, ".desired.json.tmp-789"),
	} {
		writeFile(t, p, "x")
	}
	for _, p := range []string{
		env.Home.Join(home.TmpDir, "plan-123"),
		env.Home.Join("." + home.ManifestFile + ".tmp-456"),
		env.Home.Join(home.StateDir, ".desired.json.tmp-789"),
	} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	// A run in progress: too young to be a leftover.
	writeFile(t, env.Home.Join(home.TmpDir, "plan-now/x"), "x")
	r := only(t, leftoversCheck{}, env)
	expect(t, r, Warn, "found")
	if r.Facts["count"] != "3" || len(r.Details) != 3 || r.Hint.IsZero() {
		t.Errorf("result: %+v", r)
	}
	if _, err := os.Stat(env.Home.Join(home.TmpDir, "plan-123")); err != nil {
		t.Errorf("the check removed a leftover: %v", err)
	}
}
