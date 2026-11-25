// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
)

// System is the computer the tools are on. Tests give their own; LocalSystem is the real one.
type System struct {
	// GOOS is the operating system, as in runtime.GOOS. Empty means runtime.GOOS.
	GOOS string
	// Home is the absolute path of the user's home directory, in the operating system's form.
	Home string
	// Getenv reads an environment variable. Nil means os.Getenv.
	Getenv func(string) string
	// LookPath finds an executable. Nil means exec.LookPath.
	LookPath func(string) (string, error)
	// Version runs "<tool> --version" with a timeout. Nil means RunVersion.
	Version func(ctx context.Context, bin string, args ...string) (string, error)
}

// LocalSystem is the computer the program runs on.
func LocalSystem() (System, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return System{}, fmt.Errorf("cannot tell where the home directory is: %w", err)
	}
	return System{Home: h}.withDefaults(), nil
}

func (s System) withDefaults() System {
	if s.GOOS == "" {
		s.GOOS = runtime.GOOS
	}
	if s.Getenv == nil {
		s.Getenv = os.Getenv
	}
	if s.LookPath == nil {
		s.LookPath = exec.LookPath
	}
	if s.Version == nil {
		s.Version = RunVersion
	}
	s.Home = filepath.Clean(s.Home)
	return s
}

// VersionTimeout is how long RunVersion lets a tool take to print its version.
const VersionTimeout = 10 * time.Second

// RunVersion runs a tool's version command without a shell, with VersionTimeout, and returns
// what it printed on both outputs (at most 64 KiB).
func RunVersion(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, VersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out := &capped{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	return out.String(), err
}

type capped struct {
	bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.Len(); room > 0 {
		c.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// readLog records the paths an adapter read through Env.ReadFile, in the package form of
// ext/adapter, so that a preview can copy them into its sandbox.
type readLog struct{ paths []string }

func (l *readLog) add(p string) {
	if !slices.Contains(l.paths, p) {
		l.paths = append(l.paths, p)
	}
}

// envFor is the Env of one adapter in a run of the engine, recording what it reads in log.
func (e *Engine) envFor(p adapter.Profile, log *readLog) adapter.Env { return e.sys.adapterEnv(p, log) }

// AdapterEnv is what the adapter with profile p sees of this computer: ReadFile and Stat reach
// the home directory and the absolute roots the profile resolves to, and nothing else; tools
// are found on PATH and asked for their version with RunVersion. The sync engine detects and
// plans with this Env, and rgd doctor detects with it too, so that both see a tool the same
// way.
func (s System) AdapterEnv(p adapter.Profile) adapter.Env { return s.withDefaults().adapterEnv(p, nil) }

// adapterEnv is AdapterEnv on a System with its defaults filled in. log, when not nil,
// records the paths read.
func (s System) adapterEnv(p adapter.Profile, log *readLog) adapter.Env {
	env := adapter.Env{GOOS: s.GOOS, Home: s.Home, Getenv: s.Getenv, LookPath: s.LookPath, Version: s.Version}
	// A home directory that is not known (or not absolute) is not the current directory:
	// nothing under "~" can be read then, which detection reports as unknown.
	homeKnown := filepath.IsAbs(s.Home)
	var allowed []string
	if homeKnown {
		allowed = append(allowed, s.Home)
	}
	if roots, err := adapter.ResolveRoots(p.Roots, env); err == nil {
		for _, r := range roots {
			if r != "~" && !strings.HasPrefix(r, "~/") {
				allowed = append(allowed, filepath.Clean(filepath.FromSlash(r)))
			}
		}
	}
	resolve := func(p string) (string, error) {
		refuse := func(why string) (string, error) {
			return "", &fs.PathError{Op: "read", Path: p, Err: fmt.Errorf("%w: %s", fs.ErrPermission, why)}
		}
		if p == "~" || strings.HasPrefix(p, "~/") {
			if !homeKnown {
				return refuse("the home directory is unknown")
			}
			rel := path.Clean(strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
			if rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, `\`) {
				return refuse("outside the home directory")
			}
			return filepath.Join(s.Home, filepath.FromSlash(rel)), nil
		}
		native := filepath.Clean(filepath.FromSlash(p))
		if !filepath.IsAbs(native) {
			return refuse(`not written as "~/…" or as an absolute path`)
		}
		for _, a := range allowed {
			if rel, err := filepath.Rel(a, native); err == nil && (rel == "." || filepath.IsLocal(rel)) {
				return native, nil
			}
		}
		return refuse("outside the home directory and the tool's roots")
	}
	env.ReadFile = func(p string) ([]byte, error) {
		full, err := resolve(p)
		if err != nil {
			return nil, err
		}
		if log != nil {
			log.add(p)
		}
		return os.ReadFile(full)
	}
	env.Stat = func(p string) (fs.FileInfo, error) {
		full, err := resolve(p)
		if err != nil {
			return nil, err
		}
		return os.Stat(full)
	}
	return env
}
