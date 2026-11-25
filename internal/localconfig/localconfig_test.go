// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package localconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// sampleFile is a configuration file as a person would write it: comments,
// tables, a key a newer version may have added.
const sampleFile = `# my settings
server = "https://hub.example.com"
lang = "zh-CN"

[repo]
url = "https://git.example.com/platform/resources.git"
branch = "main"
products = ["shop", "pay"]

[tools]
disabled = ["opencode"]

[mcp]
mode = "direct"

[future]
knob = 3
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestDefaultsWithoutFileOrEnvironment(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), FileName), envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{KeyMCPMode: "single-entry", KeyMCPCommand: "path", KeyLogLevel: LogInfo, KeyNoColor: "false", KeyLang: ""} {
		if v := c.Get(key); v.Str != want || v.Source != SourceDefault {
			t.Errorf("%s = %q from %s, want %q from default", key, v.Str, v.Source, want)
		}
	}
}

func TestFileValues(t *testing.T) {
	c, err := Load(writeConfig(t, sampleFile), envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.String(KeyRepoURL); got != "https://git.example.com/platform/resources.git" {
		t.Errorf("repo.url = %q", got)
	}
	if got := c.List(KeyRepoProducts); !slices.Equal(got, []string{"shop", "pay"}) {
		t.Errorf("repo.products = %v", got)
	}
	if v := c.Get(KeyMCPMode); v.Str != "direct" || v.Source != SourceFile {
		t.Errorf("mcp.mode = %q from %s", v.Str, v.Source)
	}
	if v := c.Get(KeyMCPCommand); v.Str != "path" || v.Source != SourceDefault {
		t.Errorf("mcp.command = %q from %s, want the default", v.Str, v.Source)
	}
	if w := c.Warnings(); len(w) != 1 || w[0].Kind != WarnUnknownKey || w[0].Key != "future.knob" {
		t.Errorf("warnings = %+v, want one for the unknown key future.knob", w)
	}
}

// Flags beat the environment, which beats the file, which beats the
// defaults.
func TestPrecedence(t *testing.T) {
	path := writeConfig(t, sampleFile)
	langVar := brand.EnvPrefix + "LANG"

	c, _ := Load(path, envOf(nil))
	if v := c.Get(KeyLang); v.Str != "zh-CN" || v.Source != SourceFile {
		t.Errorf("file only: lang = %q from %s", v.Str, v.Source)
	}
	c, _ = Load(path, envOf(map[string]string{langVar: "en"}))
	if v := c.Get(KeyLang); v.Str != "en" || v.Source != SourceEnv || v.Origin != langVar {
		t.Errorf("file and environment: lang = %q from %s (%s)", v.Str, v.Source, v.Origin)
	}
	if err := c.SetFlag(KeyLang, "--lang", "de"); err != nil {
		t.Fatal(err)
	}
	if v := c.Get(KeyLang); v.Str != "de" || v.Source != SourceFlag || v.Origin != "--lang" {
		t.Errorf("file, environment and flag: lang = %q from %s (%s)", v.Str, v.Source, v.Origin)
	}
	if err := c.SetFlag(KeyLang, "--lang", "not a tag!"); err == nil {
		t.Error("SetFlag accepted an invalid language")
	}
}

// --yes-global means the person agreed, this time. Neither a file nor a
// variable can agree for them.
func TestYesGlobalOnlyFromTheCommandLine(t *testing.T) {
	path := writeConfig(t, "yes_global = true\nlang = \"en\"\n")
	envVar := brand.EnvPrefix + "YES_GLOBAL"
	c, err := Load(path, envOf(map[string]string{envVar: "1"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Bool(KeyYesGlobal) {
		t.Fatal("yes_global is on although only the file and the environment set it")
	}
	var origins []string
	for _, w := range c.Warnings() {
		if w.Kind == WarnFlagOnly && w.Key == KeyYesGlobal {
			origins = append(origins, w.Origin)
		}
	}
	if !slices.Equal(origins, []string{path, envVar}) {
		t.Errorf("flag-only warnings from %v, want the file and %s", origins, envVar)
	}
	if c.String(KeyLang) != "en" {
		t.Error("the rest of the file was not applied")
	}
	if err := c.SetFlag(KeyYesGlobal, "--yes-global", "true"); err != nil || !c.Bool(KeyYesGlobal) {
		t.Errorf("the flag did not turn it on: %v", err)
	}
	if _, err := SaveFile(path, KeyYesGlobal, "true"); !errors.As(err, new(*NotInFileError)) {
		t.Errorf("SaveFile(yes_global) = %v, want a NotInFileError", err)
	}
}

func TestBrokenFileIsUnknownNotEmpty(t *testing.T) {
	tests := map[string]string{
		"not TOML":            "lang = \"en\"\n[repo\n",
		"an invalid value":    "lang = \"en\"\n[mcp]\nmode = \"sometimes\"\n",
		"the wrong type":      "lang = \"en\"\n[repo]\nproducts = \"shop\"\n",
		"a list of non-text":  "lang = \"en\"\n[tools]\ndisabled = [1, 2]\n",
		"a table for a value": "lang = \"en\"\n[server]\nurl = \"https://hub.example.com\"\n",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			c, err := Load(writeConfig(t, content), envOf(map[string]string{brand.EnvPrefix + "SERVER": "https://env.example.com"}))
			var fe *FileError
			if !errors.As(err, &fe) {
				t.Fatalf("err = %v, want a FileError", err)
			}
			if c == nil || c.String(KeyLang) != "" {
				t.Error("part of a file that cannot be used was applied")
			}
			if c.String(KeyServer) != "https://env.example.com" {
				t.Error("the environment was not applied")
			}
		})
	}
}

func TestUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not stop this user from reading")
	}
	path := writeConfig(t, sampleFile)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, envOf(nil)); !errors.As(err, new(*FileError)) {
		t.Errorf("err = %v, want a FileError", err)
	}
	if _, err := SaveFile(path, KeyLang, "en"); !errors.As(err, new(*FileError)) {
		t.Errorf("SaveFile on an unreadable file: err = %v, want a FileError", err)
	}
}

func TestEnvironment(t *testing.T) {
	c, err := Load("", envOf(map[string]string{"NO_COLOR": "anything", brand.EnvPrefix + "NONINTERACTIVE": "1"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Bool(KeyNoColor) || !c.Bool(KeyNonInteractive) {
		t.Errorf("no_color = %v, non_interactive = %v; want both on", c.Bool(KeyNoColor), c.Bool(KeyNonInteractive))
	}
	c, _ = Load("", envOf(map[string]string{brand.EnvPrefix + "NO_COLOR": "0"}))
	if c.Bool(KeyNoColor) {
		t.Error("RGD_NO_COLOR=0 turned colours off")
	}
	_, err = Load("", envOf(map[string]string{brand.EnvPrefix + "LOG_LEVEL": "loud"}))
	var ee *EnvError
	if !errors.As(err, &ee) || ee.Name != brand.EnvPrefix+"LOG_LEVEL" || ee.Err.Reason != ReasonEnum {
		t.Errorf("err = %v, want an EnvError for the log level", err)
	}
}

func TestHomeIsNotKeptInTheFile(t *testing.T) {
	c, err := Load(writeConfig(t, "home = \"/elsewhere\"\n"), envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Get(KeyHome).IsSet() {
		t.Error("home was taken from the file inside home")
	}
	if w := c.Warnings(); len(w) != 1 || w[0].Kind != WarnNotInFile {
		t.Errorf("warnings = %+v", w)
	}
}

func TestSaveFile(t *testing.T) {
	path := writeConfig(t, sampleFile)
	v, err := SaveFile(path, KeyToolsDisabled, "opencode, codex")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(v.List, []string{"opencode", "codex"}) || v.Source != SourceFile {
		t.Errorf("returned %+v", v)
	}
	c, err := Load(path, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.List(KeyToolsDisabled); !slices.Equal(got, []string{"opencode", "codex"}) {
		t.Errorf("tools.disabled after saving = %v", got)
	}
	if c.String(KeyServer) != "https://hub.example.com" || c.String(KeyMCPMode) != "direct" {
		t.Error("other settings were lost")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "knob = 3") {
		t.Errorf("a key this version does not know was dropped:\n%s", data)
	}
	if old, err := os.ReadFile(path + ".1"); err != nil || string(old) != sampleFile {
		t.Errorf("the previous file was not kept as %s.1: %v", FileName, err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("permissions %v, want 0600", info.Mode().Perm())
		}
	}

	// An empty value removes the key; the default applies again.
	v, err = SaveFile(path, KeyMCPMode, "")
	if err != nil || v.Str != "single-entry" || v.Source != SourceDefault {
		t.Fatalf("removing: %+v, %v", v, err)
	}
	c, _ = Load(path, envOf(nil))
	if c.Get(KeyMCPMode).Source != SourceDefault {
		t.Error("mcp.mode is still in the file")
	}
}

func TestSaveFileCreatesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", brand.Home, FileName)
	if _, err := SaveFile(path, KeyLang, "zh_TW"); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path, envOf(nil))
	if err != nil || c.String(KeyLang) != "zh-TW" {
		t.Errorf("lang = %q, %v; want the canonical zh-TW", c.String(KeyLang), err)
	}
	if _, err := os.Stat(path + ".1"); !errors.Is(err, os.ErrNotExist) {
		t.Error("a backup was made of a file that did not exist")
	}
}

func TestSaveFileRefuses(t *testing.T) {
	broken := "lang = \"en\"\n[repo\n"
	path := writeConfig(t, broken)
	if _, err := SaveFile(path, KeyLang, "zh-CN"); !errors.As(err, new(*FileError)) {
		t.Errorf("saving into a broken file: err = %v, want a FileError", err)
	}
	if data, _ := os.ReadFile(path); string(data) != broken {
		t.Error("a file that could not be parsed was overwritten")
	}

	path = writeConfig(t, sampleFile)
	for _, tt := range []struct {
		key, value string
		check      func(error) bool
	}{
		{"no.such", "x", func(err error) bool { return errors.As(err, new(*UnknownKeyError)) }},
		{KeyHome, "/x", func(err error) bool { return errors.As(err, new(*NotInFileError)) }},
		{KeyMCPMode, "sometimes", func(err error) bool {
			var ve *ValueError
			return errors.As(err, &ve) && ve.Reason == ReasonEnum && slices.Equal(ve.Allowed, []string{"single-entry", "direct"})
		}},
		{KeyServer, "hub.example.com", func(err error) bool {
			var ve *ValueError
			return errors.As(err, &ve) && ve.Reason == ReasonURL
		}},
		{KeyLang, "english!", func(err error) bool {
			var ve *ValueError
			return errors.As(err, &ve) && ve.Reason == ReasonLang
		}},
	} {
		if _, err := SaveFile(path, tt.key, tt.value); !tt.check(err) {
			t.Errorf("SaveFile(%s, %q): unexpected error %v", tt.key, tt.value, err)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != sampleFile {
		t.Error("a refused change touched the file")
	}
}
