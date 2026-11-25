// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"errors"
	"os"
	"runtime"
	"testing"

	"github.com/code2rich/rigdepot/internal/home"
)

func TestHomeMissingIsFine(t *testing.T) {
	expect(t, only(t, homeCheck{}, newTestEnv(t)), OK, "missing")
}

func TestHomePrivate(t *testing.T) {
	env := newTestEnv(t)
	mkdata(t, env)
	writeFile(t, env.Home.Join(home.ManifestFile), "{}")
	expect(t, only(t, homeCheck{}, env), OK, "ok")
}

func TestHomeTooOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no permission bits; TestHomeWindowsIgnoresBits covers it")
	}
	env := newTestEnv(t)
	dir := mkdata(t, env)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, env.Home.Join(home.ConfigFile), "")
	if err := os.Chmod(env.Home.Join(home.ConfigFile), 0o644); err != nil {
		t.Fatal(err)
	}
	rs := run(t, homeCheck{}, env)
	if len(rs) != 2 {
		t.Fatalf("want a warning for the directory and one for the file: %+v", rs)
	}
	expect(t, rs[0], Warn, "too_open")
	expect(t, rs[1], Warn, "too_open")
	if rs[0].Path != dir || rs[0].Facts["mode"] != "0755" || rs[1].Path != env.Home.Join(home.ConfigFile) {
		t.Errorf("results: %+v", rs)
	}
	// Looking changed nothing.
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o755 {
		t.Errorf("the check changed the permissions to %v", fi.Mode().Perm())
	}
}

func TestHomeWindowsIgnoresBits(t *testing.T) {
	env := newTestEnv(t)
	dir := mkdata(t, env)
	_ = os.Chmod(dir, 0o755)
	env.GOOS = "windows"
	expect(t, only(t, homeCheck{}, env), OK, "ok")
}

func TestHomeNotADirectory(t *testing.T) {
	env := newTestEnv(t)
	writeFile(t, env.Home.Path(), "not a directory")
	r := only(t, homeCheck{}, env)
	expect(t, r, Fail, "not_dir")
	if r.Hint.IsZero() {
		t.Error("no hint")
	}
}

func TestHomeUnknown(t *testing.T) {
	env := newTestEnv(t)
	env.Home, env.HomeErr = home.Dir{}, errors.New("RGD_HOME is relative")
	expect(t, only(t, homeCheck{}, env), Fail, "no_home")
	// The checks that look inside it say why they did not.
	for _, c := range []Check{configCheck{}, manifestCheck{}, intentsCheck{}, leftoversCheck{}} {
		expect(t, only(t, c, env), Skipped, "no_home")
	}
}
