// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"os"
	"runtime"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/home"
)

func TestConfigMissing(t *testing.T) {
	expect(t, only(t, configCheck{}, newTestEnv(t)), OK, "missing")
}

func TestConfigReadable(t *testing.T) {
	env := newTestEnv(t)
	writeFile(t, env.Home.Join(home.ConfigFile), "# mine\nlang = \"zh-CN\"\n\n[repo]\nurl = \"https://git.example.com/team/resources.git\"\nsomething_newer = 1\n")
	expect(t, only(t, configCheck{}, env), OK, "ok")
}

func TestConfigUnparsable(t *testing.T) {
	env := newTestEnv(t)
	p := env.Home.Join(home.ConfigFile)
	writeFile(t, p, "lang = \"zh-CN\"\n[repo\n")
	r := only(t, configCheck{}, env)
	expect(t, r, Fail, "unreadable")
	if r.Path != p || r.Hint.IsZero() {
		t.Errorf("result: %+v", r)
	}
	if data, _ := os.ReadFile(p); string(data) != "lang = \"zh-CN\"\n[repo\n" {
		t.Errorf("the file was changed: %q", data)
	}
}

func TestConfigUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not make a file unreadable on Windows")
	}
	env := newTestEnv(t)
	p := env.Home.Join(home.ConfigFile)
	writeFile(t, p, "lang = \"en\"\n")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(p, 0o600) })
	if _, err := os.ReadFile(p); err == nil {
		t.Skip("running with privileges that read any file")
	}
	expect(t, only(t, configCheck{}, env), Fail, "unreadable")
}

func TestConfigEnvironmentInvalid(t *testing.T) {
	env := newTestEnv(t)
	env.Getenv = func(k string) string {
		if k == brand.EnvPrefix+"LOG_LEVEL" {
			return "loud"
		}
		return ""
	}
	r := only(t, configCheck{}, env)
	expect(t, r, Fail, "env_invalid")
	if r.Facts["variable"] != brand.EnvPrefix+"LOG_LEVEL" {
		t.Errorf("facts: %v", r.Facts)
	}
}

func TestConfigIgnoredKeys(t *testing.T) {
	env := newTestEnv(t)
	writeFile(t, env.Home.Join(home.ConfigFile), "yes_global = true\n")
	expect(t, only(t, configCheck{}, env), Warn, "ignored_keys")
}
