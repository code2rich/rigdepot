// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestHostAdapterEnv(t *testing.T) {
	env := newTestEnv(t)
	writeFile(t, filepath.Join(env.UserHome, ".fake", "config.json"), "{}")
	a := HostAdapterEnv(runtime.GOOS, env.UserHome, env.Getenv)

	if data, err := a.ReadFile("~/.fake/config.json"); err != nil || string(data) != "{}" {
		t.Errorf("ReadFile(~/…) = %q, %v", data, err)
	}
	abs := filepath.ToSlash(filepath.Join(env.UserHome, ".fake"))
	if fi, err := a.Stat(abs); err != nil || !fi.IsDir() {
		t.Errorf("Stat(absolute) = %v", err)
	}
	if _, err := a.Stat("~/.missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file must be told apart from an unreadable one: %v", err)
	}
	if _, err := a.ReadFile("~/../elsewhere"); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("leaving the home directory: %v", err)
	}
	if _, err := a.ReadFile(".fake/config.json"); err == nil {
		t.Error("a relative path was accepted")
	}
	// The real program runs with a timeout and returns what it printed.
	out, err := a.Version(context.Background(), testenv.NeedGit(t), "--version")
	if err != nil || len(out) == 0 {
		t.Errorf("Version = %q, %v", out, err)
	}
}
