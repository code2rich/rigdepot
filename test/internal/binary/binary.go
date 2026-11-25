// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package binary builds the real programs for the tests under test/ and makes the
// local resource repositories they sync from. Those tests drive the program the way a person
// does: through its arguments, its environment and its exit code, never by calling into it.
package binary

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// goCaches are the Go variables a build inside the isolated environment needs from the real one.
// Without them the go command would look for its caches in the temporary home: an empty module
// cache cannot build offline, an empty build cache compiles everything again.
var goCaches = []string{"GOMODCACHE", "GOCACHE"}

var recorded = map[string]string{}

// RecordGoEnv notes where the real Go caches are. Call it in TestMain before testenv isolates the
// process; it only runs "go env".
func RecordGoEnv() error {
	for _, name := range goCaches {
		if v := os.Getenv(name); v != "" {
			recorded[name] = v
			continue
		}
		out, err := exec.Command("go", "env", name).Output()
		if err != nil {
			return fmt.Errorf("go env %s: %w", name, err)
		}
		if v := strings.TrimSpace(string(out)); v != "" {
			recorded[name] = v
		}
	}
	return nil
}

// Root returns the root of the module: the directory with go.mod above the test's directory.
func Root(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("binary: no go.mod above the test's directory")
		}
		dir = parent
	}
}

// builds are the programs built so far in this test binary, by name.
var (
	buildMu sync.Mutex
	builds  = map[string]*build{}
)

type build struct {
	once sync.Once
	path string
	err  error
}

// Build compiles ./cmd/rgd once per test binary, offline, and returns the path of the program.
// It lives in the isolated environment's temporary directory and goes away with it.
func Build(t testing.TB) string { return BuildProgram(t, brand.CLI) }

// BuildServer compiles ./cmd/rgd-server the same way, for the tests that need a real server.
func BuildServer(t testing.TB) string { return BuildProgram(t, brand.Server) }

// BuildProgram compiles ./cmd/<name> once per test binary, offline, and returns its path.
func BuildProgram(t testing.TB, name string) string {
	t.Helper()
	env := testenv.Current(t)
	root := Root(t)
	buildMu.Lock()
	b, ok := builds[name]
	if !ok {
		b = &build{}
		builds[name] = b
	}
	buildMu.Unlock()
	b.once.Do(func() {
		dir := filepath.Join(filepath.Dir(env.Home), "build")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.err = err
			return
		}
		out := filepath.Join(dir, Exe(name))
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", out, "./cmd/"+name)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOPROXY=off", "GOFLAGS=-mod=readonly", "GOWORK=off")
		for name, v := range recorded {
			cmd.Env = append(cmd.Env, name+"="+v)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			b.err = fmt.Errorf("go build ./cmd/%s: %w\n%s", name, err, out)
			return
		}
		b.path = out
	})
	if b.err != nil {
		t.Fatal(b.err)
	}
	return b.path
}

// Exe adds the executable suffix of the operating system to a program's name.
func Exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// GitEnv is the environment for the real git in a test: no system configuration (the global one
// is in the temporary home), so that core.autocrlf, hooks or signing settings cannot change what
// a test commits.
func GitEnv() []string {
	return append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
}

// Git runs the real git in dir and fails the test when it fails.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	git := testenv.NeedGit(t)
	base := []string{"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false",
		"-c", "core.autocrlf=false", "-c", "init.defaultBranch=main"}
	cmd := exec.Command(git, append(base, args...)...)
	cmd.Dir = dir
	cmd.Env = GitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// Repo turns dir, a directory of files, into a git repository with one commit and returns dir.
func Repo(t testing.TB, dir string) string {
	t.Helper()
	Git(t, dir, "init", "-q")
	Git(t, dir, "add", "-A")
	Git(t, dir, "commit", "-q", "-m", "resources")
	return dir
}

// Commit commits everything in the repository at dir.
func Commit(t testing.TB, dir, message string) {
	t.Helper()
	Git(t, dir, "add", "-A")
	Git(t, dir, "commit", "-q", "-m", message)
}

// CopyTree copies the directory src into dst, which must not exist yet. Only directories and
// regular files are expected; anything else fails the test, since a fixture should not hold it.
func CopyTree(t testing.TB, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, info.Mode().Perm())
		default:
			return errors.New(p + ": neither a directory nor a regular file")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}
