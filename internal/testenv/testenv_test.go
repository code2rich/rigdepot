// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package testenv_test

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// helperVariable selects a helper mode when this test binary runs itself as a
// child process. It starts with the test knob prefix, so it survives the
// scrubbing of the environment.
const helperVariable = brand.EnvPrefix + "TEST_HELPER"

func TestHomeIsTemporary(t *testing.T) {
	env := testenv.Current(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if home != env.Home {
		t.Errorf("home directory = %q, want the temporary home %q", home, env.Home)
	}
	if home == env.RealHome || strings.HasPrefix(home, env.RealHome+string(filepath.Separator)) {
		t.Errorf("the temporary home %q is inside the real home %q", home, env.RealHome)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(config, env.Home) {
		t.Errorf("config directory = %q, want it inside the temporary home %q", config, env.Home)
	}
	if got, want := os.Getenv(brand.EnvPrefix+"HOME"), filepath.Join(env.Home, brand.Home); got != want {
		t.Errorf("local data directory = %q, want %q", got, want)
	}
	if err := env.Verify(); err != nil {
		t.Errorf("Verify() in an untouched environment: %v", err)
	}
}

// TestGuardFailsWhenHomePointsBackAtTheRealOne is the guard test: a test that
// points HOME back at the real home directory must turn the whole run red,
// even if every test passes.
func TestGuardFailsWhenHomePointsBackAtTheRealOne(t *testing.T) {
	if os.Getenv(helperVariable) == "restore-home" {
		// Not t.Setenv: that would put the old value back when the test ends,
		// and the point is a test that leaves HOME changed.
		real := testenv.Current(t).RealHome
		os.Setenv("HOME", real)
		os.Setenv("USERPROFILE", real)
		if err := testenv.Current(t).Verify(); err == nil {
			t.Error("Verify() = nil while HOME is the real home")
		}
		return
	}
	out, err := runSelf(t, "TestGuardFailsWhenHomePointsBackAtTheRealOne", helperVariable+"=restore-home")
	if err == nil {
		t.Fatalf("the child process passed although a test restored the real HOME:\n%s", out)
	}
	if !strings.Contains(out, "GUARD FAILED") {
		t.Errorf("the child process failed without the guard saying why:\n%s", out)
	}
}

func TestEnvironmentIsScrubbed(t *testing.T) {
	leaked := []string{
		"CLAUDE_CONFIG_DIR", "CODEX_HOME", "HTTPS_PROXY", "https_proxy", "ALL_PROXY",
		brand.EnvPrefix + "BASE_URL", brand.EnvPrefix + "CONFIG",
	}
	if os.Getenv(helperVariable) == "scrub" {
		for _, name := range leaked {
			if value, ok := os.LookupEnv(name); ok {
				t.Errorf("%s=%q survived", name, value)
			}
		}
		if os.Getenv(brand.EnvPrefix+"TEST_REAL_TOOLS") != "claude" {
			t.Errorf("the test knob %sTEST_REAL_TOOLS was removed", brand.EnvPrefix)
		}
		return
	}
	env := []string{helperVariable + "=scrub", brand.EnvPrefix + "TEST_REAL_TOOLS=claude"}
	for _, name := range leaked {
		env = append(env, name+"=leaked-from-the-developer-shell")
	}
	if out, err := runSelf(t, "TestEnvironmentIsScrubbed", env...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestExternalCommandsAreBlocked(t *testing.T) {
	for _, name := range []string{"git", "claude", "codex", "opencode", "security", "secret-tool"} {
		var stderr bytes.Buffer
		cmd := exec.Command(name, "--version")
		cmd.Stderr = &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != testenv.StubExitCode {
			t.Errorf("%s: err = %v, want exit code %d from the stand-in", name, err, testenv.StubExitCode)
			continue
		}
		if !strings.Contains(stderr.String(), "blocked external command: "+name) {
			t.Errorf("%s: stderr = %q, want it to name the blocked command", name, stderr.String())
		}
	}
}

func TestNeedGitReturnsTheRealGit(t *testing.T) {
	git := testenv.NeedGit(t)
	out, err := exec.Command(git, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version: %v", git, err)
	}
	if !strings.HasPrefix(string(out), "git version") {
		t.Errorf("%s --version printed %q", git, out)
	}
}

func TestNetworkIsLoopbackOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("a local fake server must stay reachable: %v", err)
	}
	resp.Body.Close()

	// 192.0.2.1 is reserved for documentation; nothing is sent because the
	// dial is refused before it connects.
	_, err = http.Get("http://192.0.2.1/")
	if !errors.Is(err, testenv.ErrNetworkBlocked) {
		t.Errorf("request to a non-loopback address: err = %v, want ErrNetworkBlocked", err)
	}
	if _, err := net.DefaultResolver.LookupHost(t.Context(), "example.invalid"); err == nil {
		t.Error("a name lookup succeeded; the resolver should not reach a DNS server")
	}
}

// runSelf runs one test of this binary in a child process with extra
// environment variables and returns its combined output.
func runSelf(t *testing.T, test string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+test+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
