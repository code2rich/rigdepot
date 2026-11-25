// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
	"github.com/code2rich/rigdepot/test/internal/binary"
)

func TestMain(m *testing.M) {
	if dump := os.Getenv(fakeMCPDump); dump != "" {
		// Started by rgd as the MCP server of TestMVPPath, not as the tests.
		os.Exit(fakeStdioMCP(dump))
	}
	if err := binary.RecordGoEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	os.Exit(testenv.Run(m))
}

// Exit codes of the command-line program (docs: 04-cli, exit codes).
const (
	exitOK      = 0
	exitFailure = 1
	exitPending = 3
	exitDiff    = 5
)

// The stand-ins for the AI coding tools answer "--version" the way the real tools do (the
// versions the adapters were verified with) and fail on anything else: the program detects the
// tools, it never runs them.
var fakeTools = []struct{ name, version string }{
	{"claude", "2.1.330 (Claude Code)"},
	{"codex", "codex-cli 0.172.0"},
	{"opencode", "2.4.1"},
}

// The three files the tools keep their global configuration in, inside the fixture home.
var globalConfigs = []string{".claude.json", ".codex/config.toml", ".config/opencode/opencode.jsonc"}

// world is one person's computer: a home directory with the fixture copied in, a resource
// repository, stand-ins for the tools, and the real program.
type world struct {
	t     *testing.T
	home  string
	repo  string
	calls string // where the stand-ins write any call other than --version
	rgd   string
	env   []string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	return newWorldNamed(t, "")
}

// newWorldNamed is newWorld for a test with several people, each on a computer of their own:
// name tells the worlds apart.
func newWorldNamed(t *testing.T, name string) *world {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs the real program")
	}
	root := filepath.Join(testenv.Current(t).Home, t.Name(), name)
	w := &world{
		t:     t,
		home:  filepath.Join(root, "home"),
		repo:  filepath.Join(root, "repo"),
		calls: filepath.Join(root, "tool-calls.log"),
		rgd:   binary.Build(t),
	}
	binary.CopyTree(t, filepath.Join("testdata", "home"), w.home)
	binary.CopyTree(t, filepath.Join(binary.Root(t), "examples", "resources"), w.repo)
	binary.Repo(t, w.repo)

	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range fakeTools {
		writeFakeTool(t, bin, tool.name, tool.version, w.calls)
	}
	w.env = w.environment(bin)
	t.Cleanup(w.noToolRan)
	return w
}

// environment is what the program runs with: the isolated environment of this test process,
// with every variable that leads to a home directory pointed at this world's home, the stand-ins
// first on PATH, and the real git named explicitly (the one on PATH is testenv's failing
// stand-in).
func (w *world) environment(bin string) []string {
	h := w.home
	set := map[string]string{
		"HOME":                   h,
		"USERPROFILE":            h,
		"XDG_CONFIG_HOME":        filepath.Join(h, ".config"),
		"XDG_DATA_HOME":          filepath.Join(h, ".local", "share"),
		"XDG_CACHE_HOME":         filepath.Join(h, ".cache"),
		"XDG_STATE_HOME":         filepath.Join(h, ".local", "state"),
		"APPDATA":                filepath.Join(h, "AppData", "Roaming"),
		"LOCALAPPDATA":           filepath.Join(h, "AppData", "Local"),
		"PATH":                   bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		brand.EnvPrefix + "GIT":  testenv.NeedGit(w.t),
		brand.EnvPrefix + "LANG": "en",
		"GIT_CONFIG_NOSYSTEM":    "1",
		"NO_COLOR":               "1",
	}
	drop := []string{brand.EnvPrefix + "HOME"}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if _, ok := set[name]; ok || slices.Contains(drop, name) {
			continue
		}
		if runtime.GOOS == "windows" {
			if _, ok := set[strings.ToUpper(name)]; ok {
				continue
			}
		}
		env = append(env, kv)
	}
	for name, v := range set {
		env = append(env, name+"="+v)
	}
	return env
}

// writeFakeTool writes a stand-in for an AI coding tool into dir.
func writeFakeTool(t *testing.T, dir, name, version, calls string) {
	t.Helper()
	file, script := name, fmt.Sprintf(`#!/bin/sh
if [ "$#" -eq 1 ] && [ "$1" = "--version" ]; then
  echo '%s'
  exit 0
fi
echo "%s $*" >> '%s'
echo "e2e: the real %s must not run" >&2
exit %d
`, version, name, calls, name, testenv.StubExitCode)
	if runtime.GOOS == "windows" {
		file = name + ".cmd"
		script = strings.ReplaceAll(fmt.Sprintf(`@echo off
if "%%~1"=="--version" if "%%~2"=="" goto version
echo %s %%*>>"%s"
echo e2e: the real %s must not run 1>&2
exit /b %d
:version
echo %s
`, name, calls, name, testenv.StubExitCode, version), "\n", "\r\n")
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// noToolRan fails the test when the program ran a tool for anything but its version.
func (w *world) noToolRan() {
	data, err := os.ReadFile(w.calls)
	if err == nil && len(data) > 0 {
		w.t.Errorf("the program ran an AI coding tool, which it must never do:\n%s", data)
	}
}

type result struct {
	code           int
	stdout, stderr string
}

// runInput runs the program with args in this world, with stdin as its standard input, which is
// not a terminal, as in a script.
func (w *world) runInput(stdin string, args ...string) result {
	w.t.Helper()
	cmd := exec.Command(w.rgd, args...)
	cmd.Dir = w.home
	cmd.Env = w.env
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			w.t.Fatalf("%s %s: %v", brand.CLI, strings.Join(args, " "), err)
		}
		code = ee.ExitCode()
	}
	r := result{code, stdout.String(), stderr.String()}
	w.t.Logf("$ %s %s  -> exit %d", brand.CLI, strings.Join(args, " "), code)
	return r
}

// must runs the program and fails the test unless it exits with want.
func (w *world) must(want int, args ...string) result {
	w.t.Helper()
	return w.mustInput(want, "", args...)
}

// mustInput is must with stdin as the program's standard input.
func (w *world) mustInput(want int, stdin string, args ...string) result {
	w.t.Helper()
	r := w.runInput(stdin, args...)
	if r.code != want {
		w.t.Fatalf("%s %s: exit code %d, want %d\nstdout:\n%s\nstderr:\n%s", brand.CLI, strings.Join(args, " "), r.code, want, r.stdout, r.stderr)
	}
	return r
}

func (w *world) path(rel string) string { return filepath.Join(w.home, filepath.FromSlash(rel)) }

func (w *world) read(rel string) []byte {
	w.t.Helper()
	data, err := os.ReadFile(w.path(rel))
	if err != nil {
		w.t.Fatal(err)
	}
	return data
}

func (w *world) exists(rel string) bool {
	_, err := os.Lstat(w.path(rel))
	return err == nil
}

// dataDir is the program's own directory in the home, relative to it.
var dataDir = brand.Home

// The places the program writes for the example repository, relative to the home.
func skillDir(tool, code string) string {
	switch tool {
	case "claude":
		return ".claude/skills/" + brand.Prefix + code
	case "codex":
		return ".agents/skills/" + brand.Prefix + code
	}
	panic(tool)
}

// TestSyncAndUninstallRestoreEveryByte is the whole path without a server: preview, sync into the
// three tools' real-looking configuration, check what was written, and uninstall back to the
// exact bytes the person had.
func TestSyncAndUninstallRestoreEveryByte(t *testing.T) {
	w := newWorld(t)
	before := snapshot(t, w.home)

	// --plan changes nothing anywhere in the home. Reading the repository fills the program's
	// cache; that, and nothing else, may appear in its own directory.
	w.must(exitOK, "sync", "--repo", w.repo, "--plan")
	afterPlan := snapshot(t, w.home)
	if d := diffTrees(before, afterPlan.without(dataDir)); d != "" {
		t.Fatalf("sync --plan changed the home directory:\n%s", d)
	}
	for p := range afterPlan.under(dataDir) {
		if p != dataDir && !strings.HasPrefix(p, dataDir+"/cache") && !strings.HasPrefix(p, dataDir+"/tmp") {
			t.Errorf("sync --plan wrote %s; a preview may fill the cache and nothing else", p)
		}
	}
	// With changes waiting, --exit-code says so.
	w.must(exitDiff, "sync", "--repo", w.repo, "--plan", "--exit-code")

	// Without a terminal and without --yes-global, global configuration is not touched, the
	// rest is written, and the exit code says something waits for the person.
	w.must(exitPending, "sync", "--repo", w.repo)
	for _, f := range globalConfigs {
		if !bytes.Equal(w.read(f), before[f].data) {
			t.Errorf("%s changed without consent", f)
		}
	}
	for _, d := range []string{skillDir("claude", "code-review"), skillDir("codex", "unit-testing")} {
		if !w.exists(d + "/SKILL.md") {
			t.Errorf("%s was not written; new files need no consent", d)
		}
	}

	w.must(exitOK, "sync", "--repo", w.repo, "--yes-global")
	w.checkWritten(before)

	// The sync kept how to reach the MCP servers: a call names the personal secret that is
	// missing on a computer that is not signed in, instead of failing to start the server.
	if r := w.must(exitPending, "mcp", "call", "jira", "search_issues", "{}"); !strings.Contains(r.stderr, "JIRA_API_TOKEN") || !strings.Contains(r.stderr, "login") {
		t.Errorf("rgd mcp call without the secret:\n%s", r.stderr)
	}

	// Everything is as wanted now.
	w.must(exitOK, "status", "--repo", w.repo)
	w.must(exitOK, "sync", "--repo", w.repo, "--plan", "--exit-code")

	w.must(exitOK, "uninstall")
	if d := diffTrees(before, snapshot(t, w.home)); d != "" {
		t.Fatalf("after uninstall the home directory is not what it was:\n%s", d)
	}
}

// checkWritten looks at what a full sync of the example repository left in the fixture home: our
// entries are there in each tool's own format, and what the person had is untouched around them.
func (w *world) checkWritten(before tree) {
	t := w.t
	t.Helper()

	// Skills: in Claude Code's directory (OpenCode reads it too) and in the one Codex reads.
	// The optional release-notes is not delivered until someone turns it on.
	for _, tool := range []string{"claude", "codex"} {
		for _, code := range []string{"code-review", "unit-testing"} {
			got := w.read(skillDir(tool, code) + "/SKILL.md")
			want, err := os.ReadFile(filepath.Join(w.repo, "skills", code, "SKILL.md"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s/SKILL.md is not the repository's", skillDir(tool, code))
			}
		}
		if w.exists(skillDir(tool, "release-notes")) {
			t.Errorf("%s: an optional resource was delivered without being turned on", skillDir(tool, "release-notes"))
		}
	}
	if w.exists(".config/opencode/skills/" + brand.Prefix + "code-review") {
		t.Error("OpenCode got its own copy of a skill it already reads from Claude Code's directory")
	}
	// The person's own skill with the same code as one of ours is theirs.
	if !bytes.Equal(w.read(".agents/skills/release-notes/SKILL.md"), before[".agents/skills/release-notes/SKILL.md"].data) {
		t.Error("the person's own release-notes skill changed")
	}

	// Claude Code: strict JSON, our servers next to the person's, the project's server with one
	// of our names untouched, the secret as a reference to an environment variable.
	var claude struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
		Projects   map[string]struct {
			MCPServers map[string]any `json:"mcpServers"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(w.read(".claude.json"), &claude); err != nil {
		t.Fatalf(".claude.json is no longer JSON: %v", err)
	}
	for _, name := range []string{"notes", "issues", brand.Prefix + "jira", brand.Prefix + "gitlab"} {
		if claude.MCPServers[name] == nil {
			t.Errorf(".claude.json: mcpServers.%s is missing", name)
		}
	}
	if h, _ := claude.MCPServers[brand.Prefix+"jira"]["headers"].(map[string]any); h["Authorization"] != "Bearer ${JIRA_API_TOKEN}" {
		t.Errorf(".claude.json: the Jira server's Authorization header is %v, want a reference to JIRA_API_TOKEN", h["Authorization"])
	}
	if p := claude.Projects["/Users/example/src/app"].MCPServers; len(p) != 1 {
		t.Errorf(".claude.json: the project's own servers changed: %v", p)
	}

	// Codex: the byte order mark, the CRLF line endings and every line the person wrote are
	// still there; our tables come after them.
	codex := w.read(".codex/config.toml")
	old := before[".codex/config.toml"].data
	if !bytes.HasPrefix(codex, old) {
		t.Error("config.toml: the person's part was changed; our tables belong after it")
	}
	added := string(codex[len(old):])
	if strings.Contains(strings.ReplaceAll(added, "\r\n", ""), "\n") {
		t.Errorf("config.toml: what we added does not use the file's CRLF line endings:\n%q", added)
	}
	for _, want := range []string{"[mcp_servers." + brand.Prefix + "jira]", "[mcp_servers." + brand.Prefix + "gitlab]", `bearer_token_env_var = "JIRA_API_TOKEN"`} {
		if !strings.Contains(added, want) {
			t.Errorf("config.toml: %q is missing from what was added:\n%s", want, added)
		}
	}

	// OpenCode: the comments and trailing commas stay, our servers go next to the person's under
	// mcp.servers, with the secret written {env:NAME}.
	oc := string(w.read(".config/opencode/opencode.jsonc"))
	for _, want := range []string{"// Default model for every project.", "/* Issue tracker of the team. */",
		`"` + brand.Prefix + `jira"`, `"` + brand.Prefix + `gitlab"`, "{env:JIRA_API_TOKEN}", `"scratch"`} {
		if !strings.Contains(oc, want) {
			t.Errorf("opencode.jsonc: %q is missing", want)
		}
	}
	if strings.Index(oc, `"`+brand.Prefix+`jira"`) < strings.Index(oc, `"servers"`) {
		t.Error("opencode.jsonc: our server is not under mcp.servers")
	}

	// Nothing the person keeps their login in was read into or changed by a sync.
	for _, f := range []string{".claude/.credentials.json", ".codex/auth.json", ".local/share/opencode/auth.json"} {
		if !bytes.Equal(w.read(f), before[f].data) {
			t.Errorf("%s changed", f)
		}
	}
}

// TestDriftIsReportedAndOverwriteKeepsTheChange: a skill changed by hand is reported by status,
// left alone by sync, and replaced by sync --overwrite only after the changed copy is kept.
func TestDriftIsReportedAndOverwriteKeepsTheChange(t *testing.T) {
	w := newWorld(t)
	before := snapshot(t, w.home)
	w.must(exitOK, "sync", "--repo", w.repo, "--yes-global")

	skill := skillDir("claude", "code-review") + "/SKILL.md"
	edited := append(w.read(skill), []byte("\nAlso check the changelog.\n")...)
	if err := os.WriteFile(w.path(skill), edited, 0o644); err != nil {
		t.Fatal(err)
	}

	// status reports it, with and without the repository.
	r := w.must(exitPending, "status")
	if !strings.Contains(r.stdout, "code-review") {
		t.Errorf("status does not name the changed resource:\n%s", r.stdout)
	}
	w.must(exitPending, "status", "--repo", w.repo)
	r = w.must(exitPending, "--json", "status", "--repo", w.repo)
	if !json.Valid([]byte(r.stdout)) {
		t.Errorf("status --json is not JSON:\n%s", r.stdout)
	}

	// A sync leaves it alone and says so.
	w.must(exitPending, "sync", "--repo", w.repo)
	if !bytes.Equal(w.read(skill), edited) {
		t.Fatal("sync overwrote a skill changed by hand without --overwrite")
	}

	// --overwrite puts the repository's version back and keeps the person's.
	w.must(exitOK, "sync", "--repo", w.repo, "--overwrite", "code-review")
	want, err := os.ReadFile(filepath.Join(w.repo, "skills", "code-review", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w.read(skill), want) {
		t.Error("sync --overwrite did not restore the repository's version")
	}
	kept := w.keptCopies(filepath.Base(filepath.Dir(skill)), "SKILL.md")
	if len(kept) != 1 || !bytes.Equal(kept[0], edited) {
		t.Fatalf("kept/ should hold exactly the changed SKILL.md once; found %d copies", len(kept))
	}
	w.must(exitOK, "status", "--repo", w.repo)

	// Uninstall restores the home; the kept copy stays behind in the program's directory, and
	// nothing else of it does.
	w.must(exitOK, "uninstall")
	after := snapshot(t, w.home)
	if d := diffTrees(before, after.without(dataDir)); d != "" {
		t.Fatalf("after uninstall the home directory is not what it was:\n%s", d)
	}
	for p := range after.under(dataDir) {
		if p != dataDir && p != dataDir+"/kept" && !strings.HasPrefix(p, dataDir+"/kept/") {
			t.Errorf("uninstall left %s behind", p)
		}
	}
	if len(w.keptCopies(filepath.Base(filepath.Dir(skill)), "SKILL.md")) != 1 {
		t.Error("uninstall lost the kept copy of the changed skill")
	}
}

// keptCopies returns the contents of every file called name in a directory called dir anywhere
// under the program's kept/ directory.
func (w *world) keptCopies(dir, name string) [][]byte {
	var out [][]byte
	root := w.path(dataDir + "/kept")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == name && filepath.Base(filepath.Dir(p)) == dir {
			if data, err := os.ReadFile(p); err == nil {
				out = append(out, data)
			}
		}
		return nil
	})
	return out
}

// TestEnableAndDisableAnOptionalResource: an optional resource arrives only once it is turned
// on, leaves when it is turned off again, and a required one cannot be turned off.
func TestEnableAndDisableAnOptionalResource(t *testing.T) {
	w := newWorld(t)
	before := snapshot(t, w.home)
	w.must(exitOK, "sync", "--repo", w.repo, "--yes-global")
	for _, tool := range []string{"claude", "codex"} {
		if w.exists(skillDir(tool, "release-notes")) {
			t.Fatalf("%s: delivered before it was turned on", skillDir(tool, "release-notes"))
		}
	}

	r := w.must(exitOK, "--json", "enable", "release-notes", "--repo", w.repo)
	var doc struct {
		Changed, Delivered bool
		Policy             string
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || !doc.Changed || !doc.Delivered || doc.Policy != "optional" {
		t.Fatalf("enable --json = %s (%v)", r.stdout, err)
	}
	// Turning on changes only the selection; the next sync installs it.
	if w.exists(skillDir("claude", "release-notes")) {
		t.Fatal("enable wrote into a tool; only sync does that")
	}
	w.must(exitOK, "sync", "--repo", w.repo)
	for _, tool := range []string{"claude", "codex"} {
		if !w.exists(skillDir(tool, "release-notes") + "/scripts/collect.sh") {
			t.Errorf("%s: not delivered after enable and sync", skillDir(tool, "release-notes"))
		}
	}

	w.must(exitOK, "disable", "release-notes", "--repo", w.repo)
	w.must(exitOK, "sync", "--repo", w.repo)
	for _, tool := range []string{"claude", "codex"} {
		if w.exists(skillDir(tool, "release-notes")) {
			t.Errorf("%s: still there after disable and sync", skillDir(tool, "release-notes"))
		}
	}

	// Jira is required by the repository: this computer cannot turn it off.
	r = w.must(exitFailure, "disable", "jira", "--repo", w.repo)
	if !strings.Contains(r.stderr, "jira") {
		t.Errorf("disable of a required resource does not say which:\n%s", r.stderr)
	}

	w.must(exitOK, "uninstall")
	if d := diffTrees(before, snapshot(t, w.home)); d != "" {
		t.Fatalf("after uninstall the home directory is not what it was:\n%s", d)
	}
}
