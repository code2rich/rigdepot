// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// TestAdapterEnv: the Env rgd doctor and the engine share reads the home directory and the
// roots the profile resolves to, tells "missing" from "not allowed", and runs a tool's version
// command.
func TestAdapterEnv(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir() // a root moved out of the home directory by the tool's variable
	for _, f := range []string{filepath.Join(home, ".fake", "config.json"), filepath.Join(outside, "settings.json")} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vars := map[string]string{"FAKE_CONFIG_DIR": outside}
	profile := adapter.Profile{ID: "fake", Roots: map[string]adapter.Root{"config": {Env: "FAKE_CONFIG_DIR", Default: "~/.fake"}}}
	a := System{GOOS: runtime.GOOS, Home: home, Getenv: func(k string) string { return vars[k] }}.AdapterEnv(profile)

	if data, err := a.ReadFile("~/.fake/config.json"); err != nil || string(data) != "{}" {
		t.Errorf("ReadFile(~/…) = %q, %v", data, err)
	}
	if fi, err := a.Stat(filepath.ToSlash(filepath.Join(home, ".fake"))); err != nil || !fi.IsDir() {
		t.Errorf("Stat(absolute, in the home directory) = %v", err)
	}
	if data, err := a.ReadFile(filepath.ToSlash(filepath.Join(outside, "settings.json"))); err != nil || string(data) != "{}" {
		t.Errorf("ReadFile(the profile's root outside the home directory) = %q, %v", data, err)
	}
	if _, err := a.Stat("~/.missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file must be told apart from an unreadable one: %v", err)
	}
	if _, err := a.ReadFile("~/../elsewhere"); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("leaving the home directory: %v", err)
	}
	if _, err := a.ReadFile(filepath.ToSlash(filepath.Join(filepath.Dir(home), "elsewhere"))); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("an absolute path outside the home directory and the roots: %v", err)
	}
	if _, err := a.ReadFile(".fake/config.json"); err == nil {
		t.Error("a relative path was accepted")
	}
	// The real program runs with a timeout and returns what it printed.
	if out, err := a.Version(context.Background(), testenv.NeedGit(t), "--version"); err != nil || len(out) == 0 {
		t.Errorf("Version = %q, %v", out, err)
	}

	// Without a known home directory nothing under ~ is read, and it is not "missing".
	unknown := System{GOOS: runtime.GOOS, Getenv: func(string) string { return "" }}.AdapterEnv(profile)
	if _, err := unknown.Stat("~/.fake"); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("unknown home directory: %v", err)
	}
}
