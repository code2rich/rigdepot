// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/manifest"
)

func TestIntentsNone(t *testing.T) {
	env := newTestEnv(t)
	expect(t, only(t, intentsCheck{}, env), OK, "none")
	// With nothing to read, the check takes no lock and creates nothing.
	if _, err := os.Lstat(env.Home.Path()); !os.IsNotExist(err) {
		t.Errorf("the local data directory was created: %v", err)
	}
	writeManifest(t, env)
	expect(t, only(t, intentsCheck{}, env), OK, "none")
}

// An interrupted write left an intent behind (Recovery.Unresolved stay in the log as well).
func TestIntentsPending(t *testing.T) {
	env := newTestEnv(t)
	l, err := manifest.Open(env.Home).Lock(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []manifest.IntentKind{manifest.Write, manifest.Remove} {
		_, err := l.Intend(manifest.Intent{Kind: kind, Entry: manifest.Entry{
			ID: "claude-code:tree:~/.claude/skills/" + brand.Prefix + string(kind), Tool: "claude-code",
			Op: manifest.OpTree, Path: "~/.claude/skills/" + brand.Prefix + string(kind),
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	l.Release()

	r := only(t, intentsCheck{}, env)
	expect(t, r, Warn, "pending")
	if len(r.Details) != 2 || r.Details[0].Key != "doctor.intents.item_write" || r.Details[1].Key != "doctor.intents.item_remove" {
		t.Errorf("details: %+v", r.Details)
	}
	if r.Facts["count"] != "2" || r.Hint.IsZero() {
		t.Errorf("result: %+v", r)
	}
	// The log is left as it was.
	l, err = manifest.Open(env.Home).Lock(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if p, _ := l.Pending(); len(p) != 2 {
		t.Errorf("%d intents left in the log", len(p))
	}
}

// Another program holds the lock: its intents may be in progress, so the answer is unknown.
func TestIntentsBusy(t *testing.T) {
	env := newTestEnv(t)
	writeFile(t, env.Home.Join(home.JournalFile), "")
	l, err := manifest.Open(env.Home).Lock(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	expect(t, only(t, intentsCheck{}, env), Unknown, "busy")
}

func TestIntentsLogDamaged(t *testing.T) {
	env := newTestEnv(t)
	writeFile(t, env.Home.Join(home.JournalFile), "not json\n{\"id\":\"x\"}\n")
	r := only(t, intentsCheck{}, env)
	expect(t, r, Fail, "unreadable")
	if r.Hint.IsZero() {
		t.Error("no hint")
	}
}
