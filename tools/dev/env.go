// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// findRoot walks up from the working directory to the directory that holds
// go.mod and brand.json.
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if isFile(filepath.Join(dir, "go.mod")) && isFile(filepath.Join(dir, "brand.json")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run this from inside the repository (no go.mod with brand.json found)")
		}
		dir = parent
	}
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// sandbox is the environment checks and tests run in: a temporary home
// directory, the real Go caches, and a Go command that cannot reach the
// network or modify go.mod.
type sandbox struct {
	root string   // repository root
	home string   // temporary home directory
	env  []string // environment for child processes
}

// newSandbox records where the real Go caches are before pointing the home
// directory somewhere else; otherwise every run would download and compile
// everything again.
func newSandbox(root string) (*sandbox, error) {
	modCache, err := goEnv(root, "GOMODCACHE")
	if err != nil {
		return nil, err
	}
	buildCache, err := goEnv(root, "GOCACHE")
	if err != nil {
		return nil, err
	}
	home, err := os.MkdirTemp("", "dev-check-home-")
	if err != nil {
		return nil, err
	}
	overrides := map[string]string{
		"HOME":            home,
		"USERPROFILE":     home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"GOMODCACHE":      modCache,
		"GOCACHE":         buildCache,
		// -buildvcs=false: the binaries built here are thrown away, and stamping
		// fails where the Git metadata is out of reach (a worktree mounted into
		// a container).
		"GOFLAGS": "-mod=readonly -buildvcs=false",
		"GOPROXY": "off",
		"GOWORK":  "off",
	}
	return &sandbox{root: root, home: home, env: mergeEnv(os.Environ(), overrides)}, nil
}

func (s *sandbox) close() error {
	return os.RemoveAll(s.home)
}

// command prepares a child process that runs in the repository root with the
// sandbox environment plus extra "KEY=value" entries.
func (s *sandbox) command(extraEnv []string, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Dir = s.root
	cmd.Env = append(append([]string{}, s.env...), extraEnv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// mergeEnv returns base with the given variables replaced. Names are compared
// case-insensitively on Windows, where the environment is case-insensitive.
func mergeEnv(base []string, overrides map[string]string) []string {
	norm := func(name string) string {
		if filepath.Separator == '\\' {
			return strings.ToUpper(name)
		}
		return name
	}
	replaced := make(map[string]bool, len(overrides))
	for name := range overrides {
		replaced[norm(name)] = true
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if !replaced[norm(name)] {
			out = append(out, kv)
		}
	}
	for name, value := range overrides {
		out = append(out, name+"="+value)
	}
	return out
}

func goEnv(root, name string) (string, error) {
	out, err := output(root, nil, "go", "env", name)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", fmt.Errorf("go env %s is empty", name)
	}
	return out, nil
}

// output runs a command and returns its trimmed standard output. On failure
// the error carries what the command wrote to standard error.
func output(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// toolsModfile is where the versions of third-party development tools are
// pinned, so that they stay out of the main module's go.mod.
const toolsModfile = "tools/tools.mod"

// goTool returns the arguments that run a pinned development tool.
func goTool(name string, args ...string) []string {
	return append([]string{"tool", "-modfile=" + toolsModfile, name}, args...)
}
