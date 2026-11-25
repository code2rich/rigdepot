// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package testenv

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
)

// Every package with tests starts with these lines:
//
//	func TestMain(m *testing.M) { testenv.Main(m) }
//
// This product edits the configuration of other tools in the user's home
// directory. A test that reaches the real home directory can damage the
// configuration of whoever runs it, so isolation is not optional: if any part
// of it cannot be set up, no test runs.

// blockedCommands are the external programs a test must not reach by
// accident: the AI coding tools, git, and the system keychain tools. A
// stand-in that fails takes their place at the front of PATH.
var blockedCommands = []string{"claude", "codex", "opencode", "git", "security", "secret-tool"}

// toolVariables point AI coding tools at their configuration. A value left
// over from the developer's shell would lead a test straight to real files.
var toolVariables = []string{
	"CLAUDE_CONFIG_DIR", "CODEX_HOME", "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT",
	"GEMINI_CONFIG_DIR", "KIMI_CONFIG_DIR", "ZCODE_HOME",
}

// proxyVariables would route a test's requests through whatever proxy the
// developer's machine uses.
var proxyVariables = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "FTP_PROXY"}

// keyringVariable chooses where the command-line program keeps the device key and token. Set to
// keyringFiles it keeps the system keychain out, so a test can never read or write the real
// macOS Keychain, Windows Credential Manager or Linux Secret Service; what it stores lands in
// the temporary home. It is the variable package keyring reads (keyring.EnvKeyring), written out
// here because this shared package cannot import that command-line one. The test of the real
// keychain clears it for itself with t.Setenv.
const (
	keyringVariable = brand.EnvPrefix + "KEYRING"
	keyringFiles    = "file"
)

// testKnobPrefix marks the variables that configure the tests themselves;
// they are the only ones with the product's prefix that survive.
const testKnobPrefix = "TEST_"

// StubExitCode is the exit code of the stand-ins for blocked commands.
const StubExitCode = 97

// Env is an isolated environment for one test binary.
type Env struct {
	// Home is the temporary home directory.
	Home string
	// RealHome is the home directory the process started with.
	RealHome string

	root         string            // temporary directory that holds Home and the stand-ins
	realCommands map[string]string // blocked command name -> where the real one is, if installed
}

var current *Env

// Main isolates the process, runs the tests and exits. It never returns.
func Main(m *testing.M) {
	os.Exit(Run(m))
}

// Run isolates the process, calls each setup function, runs the tests and
// returns the exit code. Use it instead of Main when a package has to
// prepare something of its own inside the isolated environment.
func Run(m interface{ Run() int }, setup ...func(*Env) error) int {
	env, err := Setup()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testenv: could not isolate the tests, so none were run: %v\n", err)
		return 1
	}
	code := func() int {
		for _, fn := range setup {
			if err := fn(env); err != nil {
				fmt.Fprintf(os.Stderr, "testenv: setup failed, so no tests were run: %v\n", err)
				return 1
			}
		}
		return m.Run()
	}()
	if err := env.Verify(); err != nil {
		fmt.Fprintf(os.Stderr, "testenv: GUARD FAILED: %v\n", err)
		code = 1
	}
	if err := env.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "testenv: could not remove the temporary home: %v\n", err)
		code = 1
	}
	return code
}

// Setup isolates the current process. Tests normally get this through Main.
func Setup() (*Env, error) {
	if current != nil {
		return nil, errors.New("the process is already isolated")
	}
	realHome, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot tell where the real home directory is: %w", err)
	}
	root, err := os.MkdirTemp("", brand.CLI+"-test-")
	if err != nil {
		return nil, err
	}
	// Resolve symbolic links (on macOS the temporary directory is one), so
	// that Verify compares like with like.
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return nil, err
	}
	env := &Env{
		Home:         filepath.Join(root, "home"),
		RealHome:     realHome,
		root:         root,
		realCommands: map[string]string{},
	}
	// Find the real commands before the stand-ins hide them.
	for _, name := range blockedCommands {
		if p, err := exec.LookPath(name); err == nil {
			env.realCommands[name] = p
		}
	}
	if err := env.redirectHome(); err != nil {
		return nil, err
	}
	scrubEnvironment()
	if err := os.Setenv(keyringVariable, keyringFiles); err != nil {
		return nil, err
	}
	if err := env.blockCommands(); err != nil {
		return nil, err
	}
	blockNetwork()
	current = env
	return env, nil
}

// homeVariables returns every variable through which a program finds the
// user's directories, with the value it gets inside the temporary home.
// Changing HOME alone is not enough: on Windows the home directory comes from
// USERPROFILE, and configuration from APPDATA.
func (e *Env) homeVariables() map[string]string {
	return map[string]string{
		"HOME":                   e.Home,
		"USERPROFILE":            e.Home,
		"XDG_CONFIG_HOME":        filepath.Join(e.Home, ".config"),
		"XDG_DATA_HOME":          filepath.Join(e.Home, ".local", "share"),
		"XDG_CACHE_HOME":         filepath.Join(e.Home, ".cache"),
		"XDG_STATE_HOME":         filepath.Join(e.Home, ".local", "state"),
		"APPDATA":                filepath.Join(e.Home, "AppData", "Roaming"),
		"LOCALAPPDATA":           filepath.Join(e.Home, "AppData", "Local"),
		brand.EnvPrefix + "HOME": filepath.Join(e.Home, brand.Home),
	}
}

func (e *Env) redirectHome() error {
	if err := os.MkdirAll(e.Home, 0o700); err != nil {
		return err
	}
	for name, value := range e.homeVariables() {
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}
	// On Windows these two together are another way to the home directory.
	os.Unsetenv("HOMEDRIVE")
	os.Unsetenv("HOMEPATH")
	return nil
}

// scrubEnvironment removes the variables that could lead a test to real
// configuration or onto the network.
func scrubEnvironment() {
	for _, name := range toolVariables {
		os.Unsetenv(name)
	}
	for _, name := range proxyVariables {
		os.Unsetenv(name)
		os.Unsetenv(strings.ToLower(name))
	}
	keep := brand.EnvPrefix + "HOME"
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, brand.EnvPrefix) && upper != keep && !strings.HasPrefix(upper, brand.EnvPrefix+testKnobPrefix) {
			os.Unsetenv(name)
		}
	}
}

// blockCommands puts a stand-in for every blocked command at the front of
// PATH. Running one prints which command was blocked and fails.
func (e *Env) blockCommands() error {
	dir := filepath.Join(e.root, "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range blockedCommands {
		file, script := name, "#!/bin/sh\necho \"testenv: blocked external command: "+name+"\" >&2\nexit "+fmt.Sprint(StubExitCode)+"\n"
		if runtime.GOOS == "windows" {
			file, script = name+".cmd", "@echo testenv: blocked external command: "+name+" 1>&2\r\n@exit /b "+fmt.Sprint(StubExitCode)+"\r\n"
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(script), 0o755); err != nil {
			return err
		}
	}
	return os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// ErrNetworkBlocked is the error a test gets when it dials anything but a
// loopback address.
var ErrNetworkBlocked = errors.New("testenv: network access is blocked in tests; serve the reply from a local fake server instead")

// loopbackOnly is a net.Dialer control function that lets a connection
// through only if it goes to this machine.
func loopbackOnly(network, address string, _ syscall.RawConn) error {
	if strings.HasPrefix(network, "unix") {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w (%s)", ErrNetworkBlocked, address)
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%w (%s)", ErrNetworkBlocked, address)
}

// blockNetwork makes the default HTTP transport and the default resolver
// refuse anything but loopback addresses. Code that clones the default
// transport inherits the restriction. A test that builds its own dialer from
// scratch is not covered; that is what review is for.
func blockNetwork() {
	dialer := &net.Dialer{Control: loopbackOnly}
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport.DialContext = dialer.DialContext
		transport.Proxy = nil
	}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		},
	}
}

// Verify is the guard: it fails when the process no longer points at the
// temporary home, which means a test (or the code under test) could have
// touched the real one.
func (e *Env) Verify() error {
	for name, want := range e.homeVariables() {
		if got := os.Getenv(name); got != want {
			return fmt.Errorf("%s is %q, not the temporary home %q; a test changed it and may have touched real files", name, got, want)
		}
	}
	if got := os.Getenv(keyringVariable); got != keyringFiles {
		return fmt.Errorf("%s is %q, not %q; a test changed it and may have reached the real system keychain", keyringVariable, got, keyringFiles)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot tell where the home directory is: %w", err)
	}
	if samePath(home, e.RealHome) {
		return fmt.Errorf("the home directory is the real one (%s)", home)
	}
	if !samePath(home, e.Home) {
		return fmt.Errorf("the home directory is %q, not the temporary home %q", home, e.Home)
	}
	return nil
}

func samePath(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// Close removes the temporary home.
func (e *Env) Close() error {
	if current == e {
		current = nil
	}
	return os.RemoveAll(e.root)
}

// Current returns the environment set up by Main, or fails the test when the
// package forgot its TestMain.
func Current(t testing.TB) *Env {
	t.Helper()
	if current == nil {
		t.Fatal("testenv: the tests are not isolated; add: func TestMain(m *testing.M) { testenv.Main(m) }")
	}
	return current
}

// NeedGit returns the path of the real git for a test that has to run it.
// Tests that do not ask get the failing stand-in. The test fails when git is
// not installed: a skipped test would look like a passed one.
func NeedGit(t testing.TB) string {
	t.Helper()
	path, ok := Current(t).realCommands["git"]
	if !ok {
		t.Fatal("testenv: this test needs git, and git was not found on PATH")
	}
	return path
}
