// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"context"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// writeManifest records one entry the way the program does.
func writeManifest(t *testing.T, env *Env) {
	t.Helper()
	l, err := manifest.Open(env.Home).Lock(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	m := &manifest.Manifest{}
	m.Put(manifest.Entry{
		ID:       manifest.EntryID("claude-code", manifest.OpTree, "~/.claude/skills/"+brand.Prefix+"unit-testing", nil),
		Tool:     "claude-code",
		Resource: "unit-testing",
		Version:  5,
		Op:       manifest.OpTree,
		Path:     "~/.claude/skills/" + brand.Prefix + "unit-testing",
		Digest:   "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		At:       time.Date(2026, 11, 24, 10, 2, 11, 0, time.UTC),
	})
	if err := l.Write(m); err != nil {
		t.Fatal(err)
	}
}

func TestManifestMissingIsEmpty(t *testing.T) {
	expect(t, only(t, manifestCheck{}, newTestEnv(t)), OK, "missing")
}

func TestManifestReadable(t *testing.T) {
	env := newTestEnv(t)
	writeManifest(t, env)
	r := only(t, manifestCheck{}, env)
	expect(t, r, OK, "ok")
	if r.Facts["entries"] != "1" {
		t.Errorf("facts: %v", r.Facts)
	}
}

// A manifest that cannot be read is unknown, never empty: the check fails and names why.
func TestManifestDamaged(t *testing.T) {
	for _, tt := range []struct {
		name, content, code string
	}{
		{"cut off", `{"schema":1,"entries":[`, "manifest_malformed"},
		{"empty", "", "manifest_malformed"},
		{"newer", `{"schema":99,"entries":[]}`, "manifest_newer_format"},
		{"not a manifest", `{"schema":1,"entries":"none"}`, "manifest_invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			writeFile(t, env.Home.Join(home.ManifestFile), tt.content)
			r := only(t, manifestCheck{}, env)
			expect(t, r, Fail, tt.code)
			if r.Hint.IsZero() {
				t.Error("no hint")
			}
		})
	}
}

// When the copy from before the last write exists, the hint names it.
func TestManifestDamagedPointsAtPrevious(t *testing.T) {
	env := newTestEnv(t)
	writeFile(t, env.Home.Join(home.ManifestFile), "{")
	writeFile(t, env.Home.Join(home.ManifestPrevious), `{"schema":1,"entries":[]}`)
	r := only(t, manifestCheck{}, env)
	if r.Hint.Key != "doctor.manifest.previous_hint" {
		t.Errorf("hint %q", r.Hint.Key)
	}
}
