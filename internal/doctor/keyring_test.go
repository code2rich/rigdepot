// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/keyring"
)

func withKeyring(env *Env, st keyring.Status) *Env {
	env.Keyring = func() keyring.Status { return st }
	return env
}

func TestKeyringSystem(t *testing.T) {
	r := only(t, keyringCheck{}, withKeyring(newTestEnv(t), keyring.Status{Kind: keyring.System}))
	expect(t, r, OK, "system")
	if r.Facts["kind"] != "system" {
		t.Errorf("facts %v", r.Facts)
	}
}

func TestKeyringFallbackIsAlwaysShown(t *testing.T) {
	env := newTestEnv(t)
	dir := env.Home.Join(home.SecretsDir)
	r := only(t, keyringCheck{}, withKeyring(env, keyring.Status{Kind: keyring.PlainFile, Dir: dir, Recorded: true,
		Reason: errors.New("there is no D-Bus session bus")}))
	expect(t, r, Warn, "plain_file")
	if r.Hint.IsZero() || len(r.Details) != 1 || r.Path != dir {
		t.Errorf("result %+v", r)
	}
	// Recorded earlier: no reason found now, still a warning.
	r = only(t, keyringCheck{}, withKeyring(env, keyring.Status{Kind: keyring.Protected, Dir: dir, Recorded: true}))
	expect(t, r, Warn, "protected_file")
}

func TestKeyringLooseFile(t *testing.T) {
	env := newTestEnv(t)
	p := env.Home.Join(home.SecretsDir, keyring.DeviceKey)
	rs := run(t, keyringCheck{}, withKeyring(env, keyring.Status{Kind: keyring.PlainFile, Recorded: true, Insecure: []string{p}}))
	if len(rs) != 2 {
		t.Fatalf("results %+v", rs)
	}
	expect(t, rs[0], Fail, "too_open")
	expect(t, rs[1], Warn, "plain_file")
	if rs[0].Path != p || rs[0].Hint.IsZero() {
		t.Errorf("result %+v", rs[0])
	}
}

func TestKeyringUnreachable(t *testing.T) {
	env := newTestEnv(t)
	r := only(t, keyringCheck{}, withKeyring(env, keyring.Status{Kind: keyring.System, Recorded: true,
		Err: fmt.Errorf("%w: no session bus", keyring.ErrUnavailable)}))
	expect(t, r, Fail, "unavailable")
	r = only(t, keyringCheck{}, withKeyring(env, keyring.Status{Err: errors.New("user interaction is not allowed")}))
	expect(t, r, Unknown, "unreadable")
	if r.Hint.IsZero() {
		t.Error("no hint")
	}
}

func TestKeyringWithoutHome(t *testing.T) {
	env := newTestEnv(t)
	env.Home, env.HomeErr = home.Dir{}, errors.New("no home")
	env.Keyring = func() keyring.Status { t.Fatal("looked without a local data directory"); return keyring.Status{} }
	expect(t, only(t, keyringCheck{}, env), Skipped, "no_home")
}

// Without a stand-in the check opens the real store, which inside tests has no system keychain:
// it reports the fallback and writes nothing.
func TestKeyringRealStoreInTests(t *testing.T) {
	env := newTestEnv(t)
	r := only(t, keyringCheck{}, env)
	want := "plain_file"
	if runtime.GOOS == "windows" {
		want = "protected_file"
	}
	expect(t, r, Warn, want)
	if _, err := os.Stat(env.Home.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Error("the check created the local data directory")
	}
}
