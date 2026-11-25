// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cli"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// The end-to-end tests of sync, status, uninstall, enable and disable: the program as it is
// linked (its resource kinds), run through cli.Run on a temporary home directory, against a
// resource repository made with the real git from examples/resources. The real tool adapters are
// not there yet; the tools are made from profiles, the way a tool shaped like the others is.

var verified = &adapter.Verification{Version: "1.0.0", OS: []string{"darwin", "linux", "windows"}, At: "2026-11-25", How: "a tool that exists only in these tests"}

// fakeCode takes skills, slash commands and MCP servers, in ~/.fakecode.
func fakeCode() adapter.Adapter {
	return adapter.FromProfile(adapter.Profile{
		ID: "fakecode", Name: "Fake Code",
		Detect:   adapter.Detect{Dirs: []string{"{config}"}},
		Roots:    map[string]adapter.Root{"config": {Default: "~/.fakecode"}},
		Verified: verified,
		Skills:   &adapter.SkillsTarget{Dir: "{config}/skills", Layout: adapter.LayoutDir},
		Commands: &adapter.FilesTarget{Dir: "{config}/commands", Ext: ".md"},
		MCP:      &adapter.MCPTarget{Files: []string{"{config}/settings.jsonc"}, Format: adapter.JSONC, Path: []string{"mcp", "servers"}},
	})
}

// otherAI takes skills only, and reads Fake Code's skill directory as well.
func otherAI() adapter.Adapter {
	return adapter.FromProfile(adapter.Profile{
		ID: "otherai", Name: "Other AI",
		Detect:    adapter.Detect{Dirs: []string{"{config}"}},
		Roots:     map[string]adapter.Root{"config": {Default: "~/.otherai"}},
		Verified:  verified,
		Skills:    &adapter.SkillsTarget{Dir: "{config}/skills", Layout: adapter.LayoutDir},
		AlsoReads: []adapter.SharedDir{{Kind: "skill", Dir: "~/.fakecode/skills"}},
	})
}

// notesServer is an MCP server without secrets, which a tool without environment variable
// references can take natively: its delivery is a key in the tool's global configuration.
const notesServer = `meta:
  name: Notes
  summary: Search the team's notes
  when: When you need something from the team's notes
server:
  type: stdio
  command: notes-mcp
  args: ["--read-only"]
delivery: native
`

const (
	settings   = "~/.fakecode/settings.jsonc"
	userConfig = "{\n  // mine\n  \"theme\": \"dark\",\n}\n"
)

var (
	unitDir    = "~/.fakecode/skills/" + brand.Prefix + "unit-testing"
	unitSkill  = unitDir + "/SKILL.md"
	reviewDir  = "~/.fakecode/skills/" + brand.Prefix + "code-review"
	releaseDir = "~/.fakecode/skills/" + brand.Prefix + "release-notes"
	mineSkill  = "~/.fakecode/skills/" + brand.Prefix + "mine/SKILL.md"
)

// world is a person's computer: a home directory with two AI tools that have been used, and a
// resource repository.
type world struct {
	t        *testing.T
	home     string
	repo     string
	env      map[string]string
	terminal bool
	stdin    string
	lang     string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, home: t.TempDir(), repo: filepath.Join(t.TempDir(), "resources"), env: map[string]string{}, lang: "en"}
	w.env[brand.EnvPrefix+"GIT"] = testenv.NeedGit(t)
	w.mkdir("~/.fakecode")
	w.mkdir("~/.otherai")
	// The person's own things: a skill with the prefix that is in no manifest, and settings.
	w.write(mineSkill, "my own skill\n")
	w.write(settings, userConfig)

	if err := os.CopyFS(w.repo, os.DirFS("../../examples/resources")); err != nil {
		t.Fatal(err)
	}
	w.repoWrite("mcp/notes.yaml", notesServer)
	w.git("init", "--quiet", "--initial-branch=main")
	w.commit()
	return w
}

func (w *world) git(args ...string) {
	w.t.Helper()
	cmd := exec.Command(testenv.NeedGit(w.t), append([]string{"-C", w.repo,
		"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "core.autocrlf=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		w.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (w *world) repoWrite(rel, content string) {
	w.t.Helper()
	p := filepath.Join(w.repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) commit() {
	w.t.Helper()
	w.git("add", "--all")
	w.git("commit", "--quiet", "--allow-empty", "--message", "resources")
}

type result struct {
	code           int
	stdout, stderr string
}

func (w *world) run(args ...string) result {
	w.t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Run(append([]string{"--lang", w.lang}, args...), cli.Env{
		Stdin:           strings.NewReader(w.stdin),
		Stdout:          &stdout,
		Stderr:          &stderr,
		Getenv:          func(k string) string { return w.env[k] },
		HomeDir:         func() (string, error) { return w.home, nil },
		SystemLanguages: func() []string { return nil },
		IsTerminal:      func(any) bool { return w.terminal },
		GOOS:            runtime.GOOS,
		Adapters:        []adapter.Adapter{fakeCode(), otherAI()},
	})
	return result{code, stdout.String(), stderr.String()}
}

// must runs the program and fails the test unless it exits with code.
func (w *world) must(code cliout.ExitCode, args ...string) result {
	w.t.Helper()
	r := w.run(args...)
	if r.code != int(code) {
		w.t.Fatalf("%s: exit code %d, want %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), r.code, code, r.stdout, r.stderr)
	}
	return r
}

// sync is "rgd sync --repo <repo>" with more arguments.
func (w *world) sync(code cliout.ExitCode, args ...string) result {
	w.t.Helper()
	return w.must(code, append([]string{"sync", "--repo", w.repo}, args...)...)
}

func (w *world) path(p string) string {
	return filepath.Join(w.home, filepath.FromSlash(strings.TrimPrefix(p, "~/")))
}

func (w *world) write(p, content string) {
	w.t.Helper()
	full := w.path(p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) mkdir(p string) {
	w.t.Helper()
	if err := os.MkdirAll(w.path(p), 0o755); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) read(p string) string {
	w.t.Helper()
	data, err := os.ReadFile(w.path(p))
	if err != nil {
		w.t.Fatal(err)
	}
	return string(data)
}

func (w *world) exists(p string) bool {
	_, err := os.Lstat(w.path(p))
	return err == nil
}

// shown is a "~/…" path the way the program prints it on this system.
func shown(p string) string {
	if runtime.GOOS == "windows" {
		return `%USERPROFILE%` + strings.ReplaceAll(strings.TrimPrefix(p, "~"), "/", `\`)
	}
	return p
}

// snapshot is the home directory as "~/path" → content ("<dir>" for a directory, the mode bits
// after it). With tools set, the local data directory is left out.
func (w *world) snapshot(toolsOnly bool) map[string]string {
	w.t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(w.home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(w.home, p)
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".":
			return nil
		case toolsOnly && rel == brand.Home:
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			out["~/"+rel] = "<dir> " + info.Mode().String()
			return nil
		}
		data, err := os.ReadFile(p)
		out["~/"+rel] = string(data) + " " + info.Mode().String()
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func diffSnapshots(before, after map[string]string) []string {
	var out []string
	for p, a := range after {
		if b, ok := before[p]; !ok {
			out = append(out, "created "+p)
		} else if a != b {
			out = append(out, "changed "+p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			out = append(out, "removed "+p)
		}
	}
	slices.Sort(out)
	return out
}

// document decodes standard output as exactly one JSON document with the given schema.
func document(t *testing.T, r result, schema string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(r.stdout))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("standard output is not a JSON document: %v\n%s", err, r.stdout)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		t.Fatalf("standard output holds more than one JSON document:\n%s", r.stdout)
	}
	if doc["schema"] != schema {
		t.Fatalf("schema %v, want %s", doc["schema"], schema)
	}
	return doc
}

// inOrder fails unless every string is in s, each after the one before.
func inOrder(t *testing.T, s string, want ...string) {
	t.Helper()
	at := 0
	for _, w := range want {
		i := strings.Index(s[at:], w)
		if i < 0 {
			t.Fatalf("%q is missing (or out of order) in:\n%s", w, s)
		}
		at += i + len(w)
	}
}

func unixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file permissions that make a file unreadable are a Unix thing")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
}

func (w *world) writeConfig(content string) {
	w.t.Helper()
	w.write("~/"+brand.Home+"/config.toml", content)
}

func strconvQuote(s string) string { return strconv.Quote(s) }

func (w *world) repoRead(rel string) string {
	w.t.Helper()
	data, err := os.ReadFile(filepath.Join(w.repo, filepath.FromSlash(rel)))
	if err != nil {
		w.t.Fatal(err)
	}
	return string(data)
}
