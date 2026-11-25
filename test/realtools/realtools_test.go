// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package realtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
	"github.com/code2rich/rigdepot/test/internal/binary"
)

// These tests run the AI coding tools installed on this computer against what the program writes,
// inside a temporary home directory. They are off by default and never part of check: the tools
// are whatever version happens to be installed, and they may use the network on their own
// (update checks, DNS). Turn them on by naming the tools:
//
//	RGD_TEST_REAL_TOOLS=claude,codex,opencode go test -count=1 -v ./test/realtools
//
// For each tool the test answers two questions (docs: 11-testing, "在真实工具上验证"):
//  1. How does the tool write an MCP server itself? Its own "mcp add" in one home, our sync in
//     another, and the two entries must be the same.
//  2. Does it take what we wrote? Its own list command reads our entries back. The servers are
//     stand-ins on this computer: the test binary itself as a stdio server, and an HTTP server on
//     the loopback address that records the Authorization header, so that a tool that connects
//     shows whether it replaced our reference to the secret with its value.
//
// The log ends with each tool's version; that is what an adapter profile's "verified" is
// updated from.

// toolsVariable names the tools to test.
var toolsVariable = brand.EnvPrefix + "TEST_REAL_TOOLS"

// fakeMCPVariable turns the test binary into the stdio MCP server stand-in. Its value is the file
// the stand-in appends to whenever it starts.
var fakeMCPVariable = brand.EnvPrefix + "TEST_FAKE_MCP"

// What the program and the tools are run with, recorded before testenv hides them.
var (
	realPath  string            // PATH as it was: the tools, and node for those written in it
	realTools map[string]string // tool command -> where it is installed
	wanted    []string          // tool commands named in toolsVariable
)

func TestMain(m *testing.M) {
	if out := os.Getenv(fakeMCPVariable); out != "" {
		os.Exit(serveStdio(out))
	}
	realPath = os.Getenv("PATH")
	realTools = map[string]string{}
	for _, name := range strings.Split(os.Getenv(toolsVariable), ",") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		wanted = append(wanted, name)
		if p, err := exec.LookPath(name); err == nil {
			realTools[name] = p
		}
	}
	if len(wanted) > 0 {
		if err := binary.RecordGoEnv(); err != nil {
			fmt.Fprintln(os.Stderr, "realtools:", err)
			os.Exit(1)
		}
	}
	os.Exit(testenv.Run(m))
}

// The resource repository: one stdio server, one HTTP server with a personal secret, one skill.
const (
	secretName  = "RT_DEMO_TOKEN"
	secretValue = "rt-demo-value-0b7e"
	localCode   = "rt-local"
	remoteCode  = "rt-remote"
	skillCode   = "rt-hello"
	localMode   = "check"
)

// tool is one AI coding tool under test.
type tool struct {
	command string // what it is run as
	adapter string // the adapter's identifier
	// addOwn adds the two servers with the tool's own command, under the names we use.
	addOwn func(s *suite, home string) error
	// own reads the two servers' entries from the home the tool wrote them into, or ours.
	entries func(s *suite, home string) (map[string]any, error)
	// readBack runs the tool's own list command on the home we synced into.
	readBack func(s *suite, t *testing.T)
}

var tools = []tool{
	{command: "claude", adapter: "claude-code", addOwn: claudeAdd, entries: claudeEntries, readBack: claudeList},
	{command: "codex", adapter: "codex", addOwn: codexAdd, entries: codexEntries, readBack: codexList},
	{command: "opencode", adapter: "opencode", addOwn: opencodeAdd, entries: opencodeEntries, readBack: opencodeList},
}

// suite is what all tools share: the program, the repository, the stand-in servers, and the
// home directory we synced into.
type suite struct {
	root      string
	rgd       string
	git       string // the real git, which testenv hides from PATH
	repo      string
	synced    string // the home rgd synced into
	stdio     string // command of the stdio stand-in (this test binary)
	stdioLog  string // where the stdio stand-in notes each start
	http      *httpStandIn
	summaries []string
}

func names(code string) string { return brand.Prefix + code }

func TestRealTools(t *testing.T) {
	if len(wanted) == 0 {
		t.Skipf("set %s=claude,codex,opencode to run the installed AI coding tools in a temporary home directory", toolsVariable)
	}
	s := &suite{root: filepath.Join(testenv.Current(t).Home, "realtools")}
	s.rgd = binary.Build(t)
	s.git = testenv.NeedGit(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s.stdio = exe
	s.stdioLog = filepath.Join(s.root, "stdio-starts.log")
	s.http = startHTTPStandIn(t)
	s.repo = s.makeRepo(t)

	// One sync for every tool that is installed, into one home.
	s.synced = s.newHome(t, "synced")
	r := s.run(t, s.synced, s.rgd, "sync", "--repo", s.repo, "--yes-global")
	if r.err != nil {
		t.Fatalf("%s sync: %v\n%s%s", brand.CLI, r.err, r.stdout, r.stderr)
	}
	t.Logf("%s sync:\n%s", brand.CLI, r.stdout)

	for _, name := range wanted {
		tl := lookupTool(name)
		t.Run(name, func(t *testing.T) {
			if tl == nil {
				t.Fatalf("unknown tool %q; known: claude, codex, opencode", name)
			}
			if _, ok := realTools[tl.command]; !ok {
				t.Fatalf("%s is not installed (not found on PATH)", tl.command)
			}
			version := s.version(t, tl.command)
			t.Logf("%s --version: %s", tl.command, version)
			s.summaries = append(s.summaries, fmt.Sprintf("%-9s %s", tl.command, version))

			t.Run("writes the same entries", func(t *testing.T) { s.compareShapes(t, tl) })
			t.Run("reads our entries", func(t *testing.T) { tl.readBack(s, t) })
		})
	}
	t.Logf("versions checked (%s):\n  %s", time.Now().Format("2006-01-02"), strings.Join(s.summaries, "\n  "))
}

func lookupTool(name string) *tool {
	for i := range tools {
		if tools[i].command == name {
			return &tools[i]
		}
	}
	return nil
}

// makeRepo writes the resource repository and commits it.
func (s *suite) makeRepo(t *testing.T) string {
	dir := filepath.Join(s.root, "repo")
	files := map[string]string{
		brand.RepoConfig: fmt.Sprintf("schema: 1\nname: Real tools check\nsecrets:\n  %s:\n    label: A token for the stand-in server\n", secretName),
		"mcp/" + localCode + ".yaml": fmt.Sprintf(`meta:
  name: Local stand-in
  summary: The test binary speaking MCP over standard input and output
server:
  type: stdio
  command: %s
  args: ["--flag"]
  env:
    RT_MODE: %s
    %s: %s
delivery: native
`, yamlString(s.stdio), localMode, fakeMCPVariable, yamlString(s.stdioLog)),
		"mcp/" + remoteCode + ".yaml": fmt.Sprintf(`meta:
  name: Remote stand-in
  summary: An MCP server on the loopback address
server:
  type: http
  url: %s
  headers:
    Authorization: "Bearer ${secret:%s}"
delivery: native
`, s.http.url, secretName),
		"skills/" + skillCode + "/SKILL.md":  "---\nname: " + skillCode + "\ndescription: Says hello. Use it when asked to greet someone.\n---\n\nSay hello.\n",
		"skills/" + skillCode + "/meta.yaml": "name: Hello\n",
	}
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return binary.Repo(t, dir)
}

func yamlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// newHome makes an empty home directory with the directories the tools insist on.
func (s *suite) newHome(t *testing.T, name string) string {
	h := filepath.Join(s.root, name)
	for _, d := range []string{".claude", ".codex", ".config", "work"} {
		if err := os.MkdirAll(filepath.Join(h, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// env is the environment the program and the tools run with in home: every variable that leads
// to a home or configuration directory points into it, PATH is the real one, the personal secret
// is set the way a person would set it, and the tools are asked not to update themselves or
// report anything.
func (s *suite) env(home string, extra ...string) []string {
	set := map[string]string{
		"HOME":                   home,
		"USERPROFILE":            home,
		"XDG_CONFIG_HOME":        filepath.Join(home, ".config"),
		"XDG_DATA_HOME":          filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME":         filepath.Join(home, ".cache"),
		"XDG_STATE_HOME":         filepath.Join(home, ".local", "state"),
		"APPDATA":                filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA":           filepath.Join(home, "AppData", "Local"),
		"CLAUDE_CONFIG_DIR":      filepath.Join(home, ".claude"),
		"CODEX_HOME":             filepath.Join(home, ".codex"),
		"PATH":                   realPath,
		secretName:               secretValue,
		brand.EnvPrefix + "GIT":  s.git,
		brand.EnvPrefix + "LANG": "en",
		"GIT_CONFIG_NOSYSTEM":    "1",
		"NO_COLOR":               "1",
		// Claude Code
		"DISABLE_AUTOUPDATER":                      "1",
		"DISABLE_TELEMETRY":                        "1",
		"DISABLE_ERROR_REPORTING":                  "1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		// OpenCode
		"OPENCODE_DISABLE_AUTOUPDATE": "1",
	}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if _, ok := set[name]; ok || name == brand.EnvPrefix+"HOME" {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}
	return append(env, extra...)
}

type ran struct {
	stdout, stderr string
	err            error
}

// run runs a program in home, from a project directory inside it: tools look for project
// configuration from where they start, and must not find the developer's.
func (s *suite) run(t *testing.T, home, prog string, args ...string) ran {
	return s.runEnv(t, home, nil, prog, args...)
}

func (s *suite) runEnv(t *testing.T, home string, extra []string, prog string, args ...string) ran {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Dir = filepath.Join(home, "work")
	cmd.Env = s.env(home, extra...)
	cmd.Stdin = strings.NewReader("")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return ran{stdout.String(), stderr.String(), err}
}

// tool runs a real tool and fails the test when it fails.
func (s *suite) tool(t *testing.T, home, name string, args ...string) string {
	t.Helper()
	r := s.run(t, home, realTools[name], args...)
	if r.err != nil {
		t.Fatalf("%s %s: %v\n%s%s", name, strings.Join(args, " "), r.err, r.stdout, r.stderr)
	}
	return r.stdout
}

func (s *suite) version(t *testing.T, name string) string {
	home := s.newHome(t, "version-"+name)
	return strings.TrimSpace(s.tool(t, home, name, "--version"))
}

// compareShapes adds the servers with the tool's own command in a fresh home and compares the
// entries it wrote with ours.
func (s *suite) compareShapes(t *testing.T, tl *tool) {
	own := s.newHome(t, "own-"+tl.command)
	if err := tl.addOwn(s, own); err != nil {
		t.Fatal(err)
	}
	theirs, err := tl.entries(s, own)
	if err != nil {
		t.Fatalf("reading what %s wrote: %v", tl.command, err)
	}
	ours, err := tl.entries(s, s.synced)
	if err != nil {
		t.Fatalf("reading what %s sync wrote: %v", brand.CLI, err)
	}
	for _, code := range []string{localCode, remoteCode} {
		a, b := canonical(theirs[names(code)]), canonical(ours[names(code)])
		if a != b {
			t.Errorf("%s: the tool writes\n  %s\nand we wrote\n  %s", names(code), a, b)
		} else {
			t.Logf("%s: same entry as the tool's own: %s", names(code), a)
		}
	}
}

// canonical is a value as JSON with sorted keys.
func canonical(v any) string {
	if v == nil {
		return "(missing)"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (s *suite) stdioArgs() []string { return []string{s.stdio, "--flag"} }

func (s *suite) stdioEnv() [][2]string {
	return [][2]string{{"RT_MODE", localMode}, {fakeMCPVariable, s.stdioLog}}
}

// ---- Claude Code

func claudeAdd(s *suite, home string) error {
	args := []string{"mcp", "add", "--scope", "user", "--transport", "http", names(remoteCode), s.http.url,
		"--header", "Authorization: Bearer ${" + secretName + "}"}
	if r := s.runQuiet(home, "claude", args...); r.err != nil {
		return fmt.Errorf("claude %s: %v\n%s%s", strings.Join(args, " "), r.err, r.stdout, r.stderr)
	}
	args = []string{"mcp", "add", "--scope", "user", names(localCode)}
	for _, kv := range s.stdioEnv() {
		args = append(args, "--env", kv[0]+"="+kv[1])
	}
	args = append(append(args, "--"), s.stdioArgs()...)
	if r := s.runQuiet(home, "claude", args...); r.err != nil {
		return fmt.Errorf("claude %s: %v\n%s%s", strings.Join(args, " "), r.err, r.stdout, r.stderr)
	}
	return nil
}

// runQuiet runs a real tool without a test to fail.
func (s *suite) runQuiet(home, name string, args ...string) ran {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, realTools[name], args...)
	cmd.Dir = filepath.Join(home, "work")
	cmd.Env = s.env(home)
	cmd.Stdin = strings.NewReader("")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return ran{stdout.String(), stderr.String(), err}
}

func claudeEntries(_ *suite, home string) (map[string]any, error) {
	// With CLAUDE_CONFIG_DIR set, .claude.json is inside that directory.
	data, err := os.ReadFile(filepath.Join(home, ".claude", ".claude.json"))
	if err != nil {
		return nil, err
	}
	var doc struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	return doc.MCPServers, json.Unmarshal(data, &doc)
}

func claudeList(s *suite, t *testing.T) {
	s.resetStandIns()
	out := s.tool(t, s.synced, "claude", "mcp", "list")
	t.Logf("claude mcp list:\n%s", out)
	for _, code := range []string{localCode, remoteCode} {
		line := lineWith(out, names(code)+":")
		switch {
		case line == "":
			t.Errorf("claude mcp list does not list %s", names(code))
		case !strings.Contains(line, "Connected"):
			t.Errorf("claude mcp list lists %s but did not connect: %s", names(code), line)
		}
	}
	s.checkSecret(t, "Claude Code")
	s.checkStdioStarted(t, "Claude Code")
}

// ---- Codex

func codexAdd(s *suite, home string) error {
	args := []string{"mcp", "add", names(remoteCode), "--url", s.http.url, "--bearer-token-env-var", secretName}
	if r := s.runQuiet(home, "codex", args...); r.err != nil {
		return fmt.Errorf("codex %s: %v\n%s%s", strings.Join(args, " "), r.err, r.stdout, r.stderr)
	}
	args = []string{"mcp", "add", names(localCode)}
	for _, kv := range s.stdioEnv() {
		args = append(args, "--env", kv[0]+"="+kv[1])
	}
	args = append(append(args, "--"), s.stdioArgs()...)
	if r := s.runQuiet(home, "codex", args...); r.err != nil {
		return fmt.Errorf("codex %s: %v\n%s%s", strings.Join(args, " "), r.err, r.stdout, r.stderr)
	}
	return nil
}

func codexEntries(_ *suite, home string) (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		return nil, err
	}
	var doc struct {
		MCPServers map[string]any `toml:"mcp_servers"`
	}
	return doc.MCPServers, toml.Unmarshal(data, &doc)
}

func codexList(s *suite, t *testing.T) {
	out := s.tool(t, s.synced, "codex", "mcp", "list", "--json")
	t.Logf("codex mcp list --json:\n%s", out)
	var list []struct {
		Name      string         `json:"name"`
		Enabled   bool           `json:"enabled"`
		Transport map[string]any `json:"transport"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("codex mcp list --json is not the expected JSON: %v", err)
	}
	byName := map[string]map[string]any{}
	for _, e := range list {
		if !e.Enabled {
			t.Errorf("codex lists %s as disabled", e.Name)
		}
		byName[e.Name] = e.Transport
	}
	local, remote := byName[names(localCode)], byName[names(remoteCode)]
	if local == nil || local["command"] != s.stdio || canonical(local["args"]) != `["--flag"]` {
		t.Errorf("codex reads %s as %v", names(localCode), local)
	}
	if env, _ := local["env"].(map[string]any); env["RT_MODE"] != localMode {
		t.Errorf("codex reads the environment of %s as %v", names(localCode), local["env"])
	}
	if remote == nil || remote["url"] != s.http.url || remote["bearer_token_env_var"] != secretName {
		t.Errorf("codex reads %s as %v", names(remoteCode), remote)
	}
}

// ---- OpenCode

func opencodeAdd(s *suite, home string) error {
	args := []string{"mcp", "add", "--global", names(remoteCode), "--url", s.http.url,
		"--header", "Authorization=Bearer {env:" + secretName + "}"}
	if r := s.runQuiet(home, "opencode", args...); r.err != nil {
		return fmt.Errorf("opencode %s: %v\n%s%s", strings.Join(args, " "), r.err, r.stdout, r.stderr)
	}
	args = []string{"mcp", "add", "--global", names(localCode)}
	for _, kv := range s.stdioEnv() {
		args = append(args, "--env", kv[0]+"="+kv[1])
	}
	args = append(append(args, "--"), s.stdioArgs()...)
	if r := s.runQuiet(home, "opencode", args...); r.err != nil {
		return fmt.Errorf("opencode %s: %v\n%s%s", strings.Join(args, " "), r.err, r.stdout, r.stderr)
	}
	return nil
}

func opencodeEntries(_ *suite, home string) (map[string]any, error) {
	// Both opencode mcp add --global and our sync create opencode.json in an empty home.
	data, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.json"))
	if err != nil {
		return nil, err
	}
	var doc struct {
		MCP struct {
			Servers map[string]any `json:"servers"`
		} `json:"mcp"`
	}
	return doc.MCP.Servers, json.Unmarshal(data, &doc)
}

// opencodeList: opencode mcp list talks to a server. Without --server it starts or reuses the
// background service on a fixed port, which the developer's own OpenCode may hold, and with
// --standalone it asks a fresh server that has not connected to anything yet and answers "No
// MCP servers configured". So the test starts opencode serve on a free loopback port inside the
// temporary home, waits until the server has tried every MCP server, and then runs
// opencode mcp list --server against it.
func opencodeList(s *suite, t *testing.T) {
	s.resetStandIns()
	srv := startOpencode(t, s)
	defer srv.stop()

	mcp := srv.waitForMCP(t, []string{names(localCode), names(remoteCode)})
	r := s.runEnv(t, s.synced, []string{"OPENCODE_PASSWORD=" + srv.password}, realTools["opencode"], "mcp", "list", "--server", srv.url)
	if r.err != nil {
		t.Fatalf("opencode mcp list --server: %v\n%s%s", r.err, r.stdout, r.stderr)
	}
	t.Logf("opencode mcp list --server %s:\n%s", srv.url, r.stdout)
	for _, code := range []string{localCode, remoteCode} {
		line := lineWith(r.stdout, names(code))
		switch {
		case line == "":
			t.Errorf("opencode mcp list does not list %s", names(code))
		case mcp[names(code)] != "connected":
			t.Errorf("opencode lists %s but did not connect: %s", names(code), line)
		}
	}
	s.checkSecret(t, "OpenCode")
	s.checkStdioStarted(t, "OpenCode")

	// OpenCode reads ~/.claude/skills and ~/.agents/skills as well as its own directory; our
	// skill is in the first two.
	skills := srv.skills(t)
	if !slices.Contains(skills, names(skillCode)) {
		t.Errorf("OpenCode's skill list does not have %s: %v", names(skillCode), skills)
	} else {
		t.Logf("OpenCode's skill list has %s", names(skillCode))
	}
}

// ---- shared checks

// checkSecret: the tool connected to the HTTP stand-in and sent the secret's value, which means
// it replaced our reference to the environment variable.
func (s *suite) checkSecret(t *testing.T, who string) {
	auth := s.http.authorizations()
	if len(auth) == 0 {
		t.Errorf("%s never reached the HTTP stand-in", who)
		return
	}
	want := "Bearer " + secretValue
	for _, a := range auth {
		if a != want {
			t.Errorf("%s sent Authorization %q to the HTTP stand-in, want the secret's value", who, a)
			return
		}
	}
	t.Logf("%s sent the secret's value from the environment (%d requests)", who, len(auth))
}

// resetStandIns forgets what the stand-ins saw, so that the next checks see one tool only.
func (s *suite) resetStandIns() {
	s.http.reset()
	_ = os.Remove(s.stdioLog)
}

// checkStdioStarted: the stdio stand-in was started with the environment we wrote.
func (s *suite) checkStdioStarted(t *testing.T, who string) {
	data, err := os.ReadFile(s.stdioLog)
	if err != nil || !strings.Contains(string(data), "RT_MODE="+localMode) {
		t.Errorf("%s did not start the stdio server with RT_MODE=%s (log: %q, %v)", who, localMode, data, err)
	}
}

func lineWith(out, needle string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
