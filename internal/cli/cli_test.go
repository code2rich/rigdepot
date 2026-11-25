// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/localconfig"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// Two hidden commands exist only in the test binary. exit-test ends with any
// exit code, the way the commands still to come will (check, login
// ...). consent-test shows how a command takes --yes-global.
func init() {
	register("", func(a *app) *cobra.Command {
		var afterResult bool
		cmd := &cobra.Command{
			Use:    "exit-test <code>",
			Hidden: true,
			Args:   a.exactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				n, err := strconv.Atoi(args[0])
				if err != nil {
					return err
				}
				if afterResult {
					if err := a.out.Result(cliout.Schema("exit-test", 1), struct{}{}, func(w io.Writer) error {
						_, err := fmt.Fprintln(w, "partly done")
						return err
					}); err != nil {
						return err
					}
					return cliout.Status(n)
				}
				return cliout.Fail(cliout.ExitCode(n), "test_"+args[0], "failed with "+args[0])
			},
		}
		cmd.Flags().BoolVar(&afterResult, "after-result", false, "")
		return cmd
	})
	register("", func(a *app) *cobra.Command {
		var yes bool
		cmd := &cobra.Command{
			Use:    "consent-test",
			Hidden: true,
			Args:   a.noArgs,
			RunE: func(*cobra.Command, []string) error {
				cfg, err := a.config()
				if err != nil {
					return err
				}
				if yes {
					if err := cfg.SetFlag(localconfig.KeyYesGlobal, "--yes-global", "true"); err != nil {
						return err
					}
				}
				doc := struct {
					YesGlobal bool `json:"yesGlobal"`
				}{cfg.Bool(localconfig.KeyYesGlobal)}
				return a.out.Result(cliout.Schema("consent-test", 1), doc, func(w io.Writer) error {
					_, err := fmt.Fprintln(w, doc.YesGlobal)
					return err
				})
			},
		}
		cmd.Flags().BoolVar(&yes, "yes-global", false, "")
		return cmd
	})
}

// world is the process environment a test runs the program in.
type world struct {
	home   string            // the person's home directory, inside the test's temporary home
	env    map[string]string // environment variables
	system []string          // the operating system's languages
}

func newWorld(t *testing.T) *world {
	t.Helper()
	home := filepath.Join(testenv.Current(t).Home, t.Name())
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return &world{home: home, env: map[string]string{}, system: []string{"en-US"}}
}

func (w *world) dataDir() string { return filepath.Join(w.home, brand.Home) }

func (w *world) configPath() string { return filepath.Join(w.dataDir(), localconfig.FileName) }

func (w *world) writeConfig(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(w.dataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.configPath(), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

type result struct {
	code           int
	stdout, stderr string
}

func (w *world) run(args ...string) result {
	var stdout, stderr bytes.Buffer
	code := Run(args, Env{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          &stderr,
		Getenv:          func(k string) string { return w.env[k] },
		HomeDir:         func() (string, error) { return w.home, nil },
		SystemLanguages: func() []string { return w.system },
		IsTerminal:      func(any) bool { return false },
		GOOS:            runtime.GOOS,
	})
	return result{code, stdout.String(), stderr.String()}
}

// oneDocument fails unless standard output is exactly one JSON object with a
// schema.
func oneDocument(t *testing.T, args []string, stdout string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("%q: standard output is not a JSON object: %v\n%s", args, err, stdout)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("%q: standard output holds more than one JSON document:\n%s", args, stdout)
	}
	if s, _ := doc["schema"].(string); !strings.HasPrefix(s, brand.CLI+".") {
		t.Errorf("%q: schema = %v", args, doc["schema"])
	}
	return doc
}

// One case for every row of the exit code table (docs 04-cli). The codes
// 3 to 9 are produced by commands that do not exist yet; the test command
// checks that whatever a command returns reaches the process unchanged, in
// both output modes.
func TestExitCodeTable(t *testing.T) {
	w := newWorld(t)
	broken := newWorld(t)
	broken.writeConfig(t, "lang = \"en\"\n[repo\n")
	tests := []struct {
		name string
		w    *world
		args []string
		want cliout.ExitCode
		code string // the stable code in the JSON error document
	}{
		{"0 success", w, []string{"version"}, cliout.ExitOK, ""},
		{"1 failure", broken, []string{"config", "list"}, cliout.ExitFailure, "config_unreadable"},
		{"2 usage", w, []string{"no-such-command"}, cliout.ExitUsage, "unknown_command"},
		{"3 pending", w, []string{"exit-test", "3"}, cliout.ExitPending, "test_3"},
		{"4 check failed", w, []string{"exit-test", "4"}, cliout.ExitCheckFailed, "test_4"},
		{"5 differences", w, []string{"exit-test", "5"}, cliout.ExitDiff, "test_5"},
		{"6 not logged in", w, []string{"exit-test", "6"}, cliout.ExitAuth, "test_6"},
		{"7 unreachable", w, []string{"exit-test", "7"}, cliout.ExitUnreachable, "test_7"},
		{"8 incompatible", w, []string{"exit-test", "8"}, cliout.ExitIncompatible, "test_8"},
		{"9 state unknown", w, []string{"exit-test", "9"}, cliout.ExitStateUnknown, "test_9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.w.run(tt.args...)
			if r.code != int(tt.want) {
				t.Fatalf("exit code %d, want %d\nstdout: %s\nstderr: %s", r.code, tt.want, r.stdout, r.stderr)
			}
			if tt.want != cliout.ExitOK && (r.stdout != "" || !strings.HasPrefix(r.stderr, brand.CLI+": ")) {
				t.Errorf("a failure goes to standard error only: stdout %q, stderr %q", r.stdout, r.stderr)
			}
			args := append([]string{"--json"}, tt.args...)
			r = tt.w.run(args...)
			if r.code != int(tt.want) {
				t.Fatalf("--json: exit code %d, want %d", r.code, tt.want)
			}
			doc := oneDocument(t, args, r.stdout)
			if tt.want == cliout.ExitOK {
				return
			}
			e, _ := doc["error"].(map[string]any)
			if doc["schema"] != cliout.Schema("error", 1) || e["code"] != tt.code || e["exit"] != float64(tt.want) || e["message"] == "" {
				t.Errorf("--json: error document %v, want code %q and exit %d", doc, tt.code, tt.want)
			}
		})
	}
}

// Codes such as 3 and 5 also end commands that succeeded in writing their
// result: the result is the only output.
func TestExitCodeAfterAResult(t *testing.T) {
	w := newWorld(t)
	for _, code := range []cliout.ExitCode{cliout.ExitPending, cliout.ExitDiff} {
		args := []string{"--json", "exit-test", "--after-result", strconv.Itoa(int(code))}
		r := w.run(args...)
		if r.code != int(code) {
			t.Errorf("%v: exit code %d", args, r.code)
		}
		if doc := oneDocument(t, args, r.stdout); doc["schema"] != cliout.Schema("exit-test", 1) {
			t.Errorf("%v: the document is %v, want the result", args, doc)
		}
		r = w.run(args[1:]...)
		if r.code != int(code) || r.stdout != "partly done\n" || r.stderr != "" {
			t.Errorf("%v: code %d, stdout %q, stderr %q", args[1:], r.code, r.stdout, r.stderr)
		}
	}
}

func TestJSONIsExactlyOneDocument(t *testing.T) {
	w := newWorld(t)
	broken := newWorld(t)
	broken.writeConfig(t, "[mcp]\nmode = \"sometimes\"\n")
	cases := []struct {
		w    *world
		args []string
	}{
		{w, nil},
		{w, []string{"version"}},
		{w, []string{"--version"}},
		{w, []string{"--help"}},
		{w, []string{"help"}},
		{w, []string{"help", "config"}},
		{w, []string{"help", "nope"}},
		{w, []string{"config"}},
		{w, []string{"config", "--help"}},
		{w, []string{"config", "list"}},
		{w, []string{"config", "get", "lang"}},
		{w, []string{"config", "get", "nope"}},
		{w, []string{"config", "set", "mcp.mode", "direct"}},
		{w, []string{"config", "set", "mcp.mode", "sometimes"}},
		{w, []string{"config", "set", "yes_global", "true"}},
		{w, []string{"config", "nope"}},
		{w, []string{"completion", "zsh"}},
		{w, []string{"completion", "tcsh"}},
		{w, []string{"completion"}},
		{w, []string{"version", "extra"}},
		{w, []string{"--no-such-flag"}},
		{w, []string{"--lang", "english!", "version"}},
		{w, []string{"-v", "-q", "version"}},
		{w, []string{"-v", "config", "list"}},
		{w, []string{"nope"}},
		{w, []string{"exit-test", "9"}},
		{w, []string{"exit-test", "--after-result", "5"}},
		{w, []string{"exit-test", "not-a-number"}},
		{broken, []string{"config", "list"}},
		{broken, []string{"config", "set", "lang", "en"}},
	}
	for _, c := range cases {
		args := append([]string{"--json"}, c.args...)
		r := c.w.run(args...)
		oneDocument(t, args, r.stdout)
		if strings.Contains(r.stdout, "\x1b[") {
			t.Errorf("%q: colours in JSON", args)
		}
		// The flag works anywhere on the line.
		args = append(append([]string{}, c.args...), "--json")
		if len(c.args) > 0 && c.args[0] != "exit-test" {
			r = c.w.run(args...)
			oneDocument(t, args, r.stdout)
		}
	}
}

func TestVersion(t *testing.T) {
	w := newWorld(t)
	for _, args := range [][]string{{"version"}, {"--version"}} {
		r := w.run(args...)
		if r.code != 0 || !strings.HasPrefix(r.stdout, brand.CLI+" ") || r.stderr != "" {
			t.Errorf("%v: code %d, stdout %q, stderr %q", args, r.code, r.stdout, r.stderr)
		}
	}
	r := w.run("--json", "version")
	doc := oneDocument(t, nil, r.stdout)
	for _, key := range []string{"version", "commit", "date", "modified", "go", "os", "arch"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("version document lacks %q: %v", key, doc)
		}
	}
	if doc["os"] != runtime.GOOS || doc["arch"] != runtime.GOARCH {
		t.Errorf("platform %v/%v", doc["os"], doc["arch"])
	}
}

// version and completion work even when the configuration cannot be read.
func TestBrokenConfigurationDoesNotBlockVersion(t *testing.T) {
	w := newWorld(t)
	w.writeConfig(t, "[repo\n")
	for _, args := range [][]string{{"version"}, {"completion", "bash"}, {"--help"}} {
		if r := w.run(args...); r.code != 0 {
			t.Errorf("%v: exit code %d, stderr %q", args, r.code, r.stderr)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	w := newWorld(t)
	tests := []struct {
		args []string
		want []string // in the message on standard error
	}{
		{nil, []string{"needs a command", "--help"}},
		{[]string{"sync"}, []string{"nothing to sync from", "--repo"}},
		{[]string{"sync", "extra"}, []string{"takes no arguments"}},
		{[]string{"sync", "--repo", "x", "--yes-global", "--skip-global"}, []string{"contradict"}},
		{[]string{"status", "a", "b"}, []string{"takes at most 1 argument, but got 2"}},
		{[]string{"enable"}, []string{"takes exactly 1 argument, but got 0"}},
		{[]string{"confg"}, []string{`Unknown command "confg"`, "Did you mean: config"}},
		{[]string{"config"}, []string{`"` + brand.CLI + ` config" needs a command`}},
		{[]string{"config", "remove", "x"}, []string{`Unknown command "remove"`}},
		{[]string{"version", "extra"}, []string{"takes no arguments"}},
		{[]string{"config", "get"}, []string{"takes exactly 1 argument, but got 0"}},
		{[]string{"config", "set", "lang"}, []string{"takes exactly 2 arguments, but got 1"}},
		{[]string{"--nope"}, []string{"Invalid option", "--nope"}},
		{[]string{"config", "list", "--nope"}, []string{"Invalid option", `"` + brand.CLI + ` config list --help"`}},
		{[]string{"-v", "-q", "version"}, []string{"-v and -q"}},
		{[]string{"--lang", "english!", "version"}, []string{`"english!" is not a language tag`}},
		{[]string{"completion", "tcsh"}, []string{`Unknown shell "tcsh"`, "powershell"}},
		{[]string{"help", "nope"}, []string{`Unknown command "nope"`}},
	}
	for _, tt := range tests {
		r := w.run(tt.args...)
		if r.code != int(cliout.ExitUsage) || r.stdout != "" {
			t.Errorf("%q: code %d, stdout %q", tt.args, r.code, r.stdout)
		}
		for _, want := range tt.want {
			if !strings.Contains(r.stderr, want) {
				t.Errorf("%q: stderr %q lacks %q", tt.args, r.stderr, want)
			}
		}
	}
}

func TestHelp(t *testing.T) {
	w := newWorld(t)
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		r := w.run(args...)
		if r.code != 0 || r.stderr != "" {
			t.Errorf("%v: code %d, stderr %q", args, r.code, r.stderr)
		}
		for _, want := range []string{"Usage:", "config", "version", "completion", "help", "--json", "--lang", "--home"} {
			if !strings.Contains(r.stdout, want) {
				t.Errorf("%v: help lacks %q:\n%s", args, want, r.stdout)
			}
		}
		for _, hidden := range []string{"exit-test", "consent-test", "__complete"} {
			if strings.Contains(r.stdout, hidden) {
				t.Errorf("%v: help shows the hidden command %s", args, hidden)
			}
		}
	}
	r := w.run("config", "set", "--help")
	if r.code != 0 || !strings.Contains(r.stdout, brand.CLI+" config set <key> <value>") || !strings.Contains(r.stdout, "comma") {
		t.Errorf("config set --help:\n%s", r.stdout)
	}
}

// Everything a person reads comes from the catalogue: no message key shows
// up untranslated in either required language, and Chinese help has no
// English headings.
func TestEveryCommandIsTranslated(t *testing.T) {
	for _, lang := range []string{"en", "zh-CN"} {
		w := newWorld(t)
		a, err := newApp([]string{"--lang", lang}, Env{
			Stdout: io.Discard, Stderr: io.Discard,
			Getenv: func(string) string { return "" }, HomeDir: func() (string, error) { return w.home, nil },
			SystemLanguages: func() []string { return nil }, IsTerminal: func(any) bool { return false },
		})
		if err != nil {
			t.Fatal(err)
		}
		var walk func(c *cobra.Command)
		walk = func(c *cobra.Command) {
			if !c.Hidden {
				if c.Short == "" || strings.Contains(c.Short, ".short") {
					t.Errorf("%s: %s has no translated description: %q", lang, c.CommandPath(), c.Short)
				}
				c.LocalFlags().VisitAll(func(f *pflag.Flag) {
					if f.Usage == "" || strings.HasPrefix(f.Usage, "flag.") {
						t.Errorf("%s: %s --%s has no translated description", lang, c.CommandPath(), f.Name)
					}
				})
			}
			for _, sub := range c.Commands() {
				walk(sub)
			}
		}
		root := newRoot(a)
		root.InitDefaultHelpFlag()
		walk(root)
	}
	w := newWorld(t)
	r := w.run("--lang", "zh-CN", "--help")
	for _, english := range []string{"Usage:", "Commands:", "Flags:", "Global"} {
		if strings.Contains(r.stdout, english) {
			t.Errorf("Chinese help contains %q:\n%s", english, r.stdout)
		}
	}
	if !strings.Contains(r.stdout, "用法：") {
		t.Errorf("Chinese help:\n%s", r.stdout)
	}
}

// The interface language: --lang, then RGD_LANG, then the configuration
// file, then the system, then English.
func TestLanguagePrecedence(t *testing.T) {
	langVar := brand.EnvPrefix + "LANG"
	tests := []struct {
		name    string
		file    string
		env     string
		system  []string
		args    []string
		chinese bool
	}{
		{"English by default", "", "", nil, nil, false},
		{"system language", "", "", []string{"zh-CN"}, nil, true},
		{"system Traditional Chinese falls back to zh-CN", "", "", []string{"zh-Hant-TW"}, nil, true},
		{"system language not available", "", "", []string{"fr-FR", "zh-CN"}, nil, true},
		{"file over system", `lang = "en"`, "", []string{"zh-CN"}, nil, false},
		{"environment over file", `lang = "en"`, "zh-CN", nil, nil, true},
		{"flag over environment", "", "zh-CN", nil, []string{"--lang", "en"}, false},
		{"flag", "", "", nil, []string{"--lang=zh-TW"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			w.system = tt.system
			if tt.file != "" {
				w.writeConfig(t, tt.file+"\n")
			}
			if tt.env != "" {
				w.env[langVar] = tt.env
			}
			r := w.run(append(tt.args, "no-such-command")...)
			if got := strings.Contains(r.stderr, "没有"); got != tt.chinese {
				t.Errorf("Chinese = %v, want %v: %q", got, tt.chinese, r.stderr)
			}
		})
	}
}

func TestConfigRoundTrip(t *testing.T) {
	w := newWorld(t)
	r := w.run("config", "set", "tools.disabled", "opencode,codex")
	if r.code != 0 || !strings.Contains(r.stdout, "tools.disabled") {
		t.Fatalf("set: code %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(w.configPath()); err != nil {
		t.Fatalf("the configuration file is not in the local data directory: %v", err)
	}
	r = w.run("config", "get", "tools.disabled")
	if r.code != 0 || r.stdout != "opencode,codex\n" {
		t.Errorf("get: code %d, stdout %q", r.code, r.stdout)
	}
	r = w.run("--json", "config", "get", "tools.disabled")
	doc := oneDocument(t, nil, r.stdout)
	if doc["source"] != "file" || fmt.Sprint(doc["value"]) != "[opencode codex]" {
		t.Errorf("get --json: %v", doc)
	}
	r = w.run("--json", "config", "get", "server")
	if doc := oneDocument(t, nil, r.stdout); doc["value"] != nil || doc["source"] != "default" {
		t.Errorf("an unset value: %v", doc)
	}
	if r := w.run("config", "get", "server"); r.code != 0 || r.stdout != "" {
		t.Errorf("an unset value prints nothing: %q", r.stdout)
	}

	// An environment variable still wins, and the person is told.
	w.env[brand.EnvPrefix+"REPO"] = "https://git.example.com/env.git"
	r = w.run("config", "set", "repo.url", "https://git.example.com/file.git")
	if r.code != 0 || !strings.Contains(r.stderr, brand.EnvPrefix+"REPO") {
		t.Errorf("set under an environment variable: code %d, stderr %q", r.code, r.stderr)
	}
	r = w.run("--json", "config", "list")
	doc = oneDocument(t, nil, r.stdout)
	settings, _ := doc["settings"].([]any)
	found := map[string]map[string]any{}
	for _, s := range settings {
		m := s.(map[string]any)
		found[m["key"].(string)] = m
	}
	if s := found["repo.url"]; s["value"] != "https://git.example.com/env.git" || s["source"] != "env" || s["origin"] != brand.EnvPrefix+"REPO" {
		t.Errorf("repo.url in the list: %v", s)
	}
	if _, listed := found[localconfig.KeyYesGlobal]; listed {
		t.Error("config list shows yes_global, which is not configuration")
	}
	r = w.run("config", "list")
	if r.code != 0 || !strings.Contains(r.stdout, "(not set)") || !strings.Contains(r.stdout, "environment variable "+brand.EnvPrefix+"REPO") {
		t.Errorf("config list:\n%s", r.stdout)
	}

	r = w.run("config", "set", "tools.disabled", "")
	if r.code != 0 || !strings.Contains(r.stdout, "Removed") {
		t.Errorf("remove: %q %q", r.stdout, r.stderr)
	}
	if r := w.run("config", "set", "mcp.mode", "sometimes"); r.code != int(cliout.ExitUsage) || !strings.Contains(r.stderr, "single-entry, direct") {
		t.Errorf("bad value: code %d, stderr %q", r.code, r.stderr)
	}
	if r := w.run("config", "set", "home", "/x"); r.code != int(cliout.ExitUsage) || !strings.Contains(r.stderr, "--home, "+brand.EnvPrefix+"HOME") {
		t.Errorf("home: code %d, stderr %q", r.code, r.stderr)
	}
}

func TestConfigSetDoesNotOverwriteABrokenFile(t *testing.T) {
	w := newWorld(t)
	broken := "lang = \"en\"\n[repo\n"
	w.writeConfig(t, broken)
	r := w.run("config", "set", "lang", "zh-CN")
	if r.code != int(cliout.ExitFailure) || !strings.Contains(r.stderr, "Nothing was changed") {
		t.Errorf("code %d, stderr %q", r.code, r.stderr)
	}
	if data, _ := os.ReadFile(w.configPath()); string(data) != broken {
		t.Error("the file was overwritten")
	}
}

func TestHomeOption(t *testing.T) {
	w := newWorld(t)
	other := filepath.Join(w.home, "elsewhere")
	if r := w.run("--home", other, "config", "set", "lang", "zh-CN"); r.code != 0 {
		t.Fatalf("code %d, stderr %q", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(other, localconfig.FileName)); err != nil {
		t.Errorf("--home was not used: %v", err)
	}
	if _, err := os.Stat(w.configPath()); err == nil {
		t.Error("the default directory was written although --home was given")
	}
	w.env[brand.EnvPrefix+"HOME"] = other
	if r := w.run("config", "get", "lang"); r.stdout != "zh-CN\n" {
		t.Errorf("RGD_HOME was not used: %q", r.stdout)
	}
}

// --yes-global is the person's explicit agreement. It counts only when typed
// on the command line: not from the environment, not from the file.
func TestYesGlobalOnlyFromTheCommandLine(t *testing.T) {
	w := newWorld(t)
	w.writeConfig(t, "yes_global = true\n")
	w.env[brand.EnvPrefix+"YES_GLOBAL"] = "1"

	r := w.run("consent-test")
	if r.code != 0 || r.stdout != "false\n" {
		t.Errorf("from the file and the environment: code %d, stdout %q", r.code, r.stdout)
	}
	if !strings.Contains(r.stderr, "Ignoring yes_global") || !strings.Contains(r.stderr, brand.EnvPrefix+"YES_GLOBAL") {
		t.Errorf("the person is not told that it was ignored: %q", r.stderr)
	}
	if r := w.run("consent-test", "--yes-global"); r.stdout != "true\n" {
		t.Errorf("from the command line: stdout %q", r.stdout)
	}
	for _, args := range [][]string{{"config", "set", "yes_global", "true"}, {"config", "get", "yes_global"}} {
		if r := w.run(args...); r.code != int(cliout.ExitUsage) || !strings.Contains(r.stderr, "only be given on the command line") {
			t.Errorf("%v: code %d, stderr %q", args, r.code, r.stderr)
		}
	}
}

func TestCompletion(t *testing.T) {
	w := newWorld(t)
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		r := w.run("completion", shell)
		if r.code != 0 || !strings.Contains(r.stdout, brand.CLI) || r.stderr != "" {
			t.Errorf("%s: code %d, %d bytes, stderr %q", shell, r.code, len(r.stdout), r.stderr)
		}
		r = w.run("--json", "completion", shell)
		if doc := oneDocument(t, nil, r.stdout); doc["shell"] != shell || doc["script"] == "" {
			t.Errorf("%s --json: %v", shell, doc["shell"])
		}
	}
	// The scripts ask the program for candidates at run time.
	r := w.run("__complete", "con")
	if r.code != 0 || !strings.Contains(r.stdout, "config") {
		t.Errorf("__complete: code %d, stdout %q", r.code, r.stdout)
	}
}

func TestBrokenEnvironmentVariable(t *testing.T) {
	w := newWorld(t)
	w.env[brand.EnvPrefix+"LOG_LEVEL"] = "loud"
	r := w.run("config", "list")
	if r.code != int(cliout.ExitUsage) || !strings.Contains(r.stderr, brand.EnvPrefix+"LOG_LEVEL") {
		t.Errorf("code %d, stderr %q", r.code, r.stderr)
	}
	if r := w.run("version"); r.code != 0 {
		t.Errorf("version: code %d", r.code)
	}
}

func TestRegistry(t *testing.T) {
	saved := registrations
	defer func() { registrations = saved }()
	w := newWorld(t)
	a, err := newApp(nil, Env{
		Stdout: io.Discard, Stderr: io.Discard, Getenv: func(string) string { return "" },
		HomeDir: func() (string, error) { return w.home, nil }, SystemLanguages: func() []string { return nil },
		IsTerminal: func(any) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := func(name string) builder { return func(*app) *cobra.Command { return &cobra.Command{Use: name} } }

	// Children may be registered before their parents.
	registrations = nil
	register("tool run", cmd("fast"))
	register("tool", cmd("run"))
	register("", cmd("tool"))
	root := &cobra.Command{Use: "root"}
	attach(a, root)
	if c, _, err := root.Find([]string{"tool", "run", "fast"}); err != nil || c.Name() != "fast" {
		t.Errorf("tool run fast: %v, %v", c, err)
	}

	for name, regs := range map[string][]registration{
		"a missing parent": {{parent: "nope", build: cmd("x")}},
		"a duplicate":      {{build: cmd("x")}, {build: cmd("x")}},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("attach did not panic")
				}
			}()
			registrations = regs
			attach(a, &cobra.Command{Use: "root"})
		})
	}
}
