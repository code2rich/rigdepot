// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package executor

import (
	"path/filepath"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
)

func TestLocateOnEveryPlatform(t *testing.T) {
	type want struct {
		root int
		rel  string
		ok   bool
	}
	cases := []struct {
		goos, home string
		extra      []string
		path       string
		want       want
	}{
		{"linux", "/home/u", nil, "~/.claude/skills/x", want{0, ".claude/skills/x", true}},
		{"linux", "/home/u", nil, "~", want{0, "", true}},
		{"linux", "/home/u", nil, "/home/u/.claude", want{0, ".claude", true}},
		{"linux", "/home/u", nil, "/home/user2/.claude", want{}},
		{"linux", "/home/u", []string{"/opt/claude"}, "/opt/claude/skills/x", want{1, "skills/x", true}},
		{"linux", "/home/u", []string{"/home/u/.cfg"}, "/home/u/.cfg/x", want{1, "x", true}},
		{"linux", "/home/u", nil, "/HOME/U/.claude", want{}},
		{"linux", "/home/u", nil, "~/a/../b", want{}},
		{"linux", "/home/u", nil, "~/a//b", want{}},
		{"linux", "/home/u", nil, "~/a/", want{}},
		{"linux", "/home/u", nil, "~user/x", want{}},
		{"linux", "/home/u", nil, "C:/x", want{}},
		{"darwin", "/Users/u", nil, "~/.claude", want{0, ".claude", true}},
		{"windows", `C:\Users\u`, nil, "~/.claude/skills/x", want{0, ".claude/skills/x", true}},
		{"windows", `C:\Users\u`, nil, "c:/users/U/.codex", want{0, ".codex", true}},
		{"windows", `C:\Users\u`, []string{`D:\tools\opencode`}, "D:/tools/opencode/opencode.jsonc", want{1, "opencode.jsonc", true}},
		{"windows", `C:\Users\u`, []string{`D:\tools\opencode`}, "D:/tools/opencode2/x", want{}},
		{"windows", `C:\Users\u`, nil, "/x", want{}},
		{"windows", `C:\Users\u`, nil, `~\.claude`, want{}},
		{"windows", `C:\Users\u`, []string{`\\server\share\cfg`}, "//server/share/cfg/x", want{1, "x", true}},
	}
	for _, c := range cases {
		l := newLocator(c.goos, c.home, c.extra)
		root, rel, err := l.locate(c.path)
		got := want{root, rel, err == nil}
		if !c.want.ok {
			if err == nil {
				t.Errorf("%s: locate(%q) = %d %q, want an error", c.goos, c.path, root, rel)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s: locate(%q) = %+v (%v), want %+v", c.goos, c.path, got, err, c.want)
		}
	}
}

func TestUnderIsStrictAndFollowsThePlatformsCase(t *testing.T) {
	cases := []struct {
		goos, dir, rel string
		want           bool
	}{
		{"linux", ".claude", ".claude/skills/x", true},
		{"linux", ".claude", ".claude", false},
		{"linux", ".claude", ".claude-other/x", false},
		{"linux", ".claude", ".Claude/x", false},
		{"linux", "", "x", true},
		{"windows", ".claude", ".Claude/x", true},
		{"windows", ".claude", ".claudex/y", false},
	}
	for _, c := range cases {
		if got := newLocator(c.goos, "/h", nil).under(c.dir, c.rel); got != c.want {
			t.Errorf("%s: under(%q, %q) = %v, want %v", c.goos, c.dir, c.rel, got, c.want)
		}
	}
}

func TestPrefixed(t *testing.T) {
	for p, want := range map[string]bool{
		"skills/" + brand.Prefix + "x": true,
		"skills/" + brand.Prefix:       false,
		brand.Prefix + "x/skill":       false,
		"skills/x" + brand.Prefix:      false,
	} {
		if prefixed(p) != want {
			t.Errorf("prefixed(%q) = %v", p, !want)
		}
	}
}

func TestKeptPath(t *testing.T) {
	for in, want := range map[string]string{
		"~/.claude/skills/x":   ".claude/skills/x",
		"/opt/claude/x":        "_abs/opt/claude/x",
		"D:/tools/opencode/x":  "_abs/D/tools/opencode/x",
		"//server/share/cfg/x": "_abs/server/share/cfg/x",
	} {
		if got := keptPath(in); got != filepath.FromSlash(want) {
			t.Errorf("keptPath(%q) = %q, want %q", in, got, want)
		}
	}
}
