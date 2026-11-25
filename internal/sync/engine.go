// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/executor"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// Executor is what the engine needs of *executor.Executor. Tests wrap the real one to watch it.
type Executor interface {
	Observe(t executor.Target) executor.Observed
	Apply(ctx context.Context, tool executor.Tool, op adapter.Op, pre executor.Observed, opt executor.Options) (executor.Applied, error)
	Revert(ctx context.Context, en manifest.Entry) (executor.Reverted, error)
	Settle(in manifest.Intent) manifest.Observation
	Close() error
}

// NewExecutor makes the real executor.
func NewExecutor(roots executor.Roots, st executor.State) (Executor, error) {
	return executor.New(roots, st)
}

// Config is what an Engine works with.
type Config struct {
	// Adapters are the tools; nil means every registered adapter (adapter.All).
	Adapters []adapter.Adapter
	// Data is the local data directory.
	Data home.Dir
	// System is the computer the tools are on.
	System System
	// NewExecutor makes executors; nil means NewExecutor.
	NewExecutor func(executor.Roots, executor.State) (Executor, error)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// LockWait is how long Lock waits for another program; zero means manifest.DefaultWait.
	LockWait time.Duration
}

// Engine plans, previews and applies syncs and uninstalls.
type Engine struct {
	adapters []adapter.Adapter
	data     home.Dir
	sys      System
	newExec  func(executor.Roots, executor.State) (Executor, error)
	now      func() time.Time
	wait     time.Duration
	store    *manifest.Store
}

// New returns an engine.
func New(cfg Config) (*Engine, error) {
	if cfg.Data.Path() == "" {
		return nil, errors.New("sync: no local data directory")
	}
	if !filepath.IsAbs(cfg.System.Home) {
		return nil, fmt.Errorf("sync: the home directory %q is not absolute", cfg.System.Home)
	}
	e := &Engine{
		adapters: cfg.Adapters,
		data:     cfg.Data,
		sys:      cfg.System.withDefaults(),
		newExec:  cfg.NewExecutor,
		now:      cfg.Now,
		wait:     cfg.LockWait,
		store:    manifest.Open(cfg.Data),
	}
	if e.adapters == nil {
		e.adapters = adapter.All()
	}
	e.adapters = slices.Clone(e.adapters)
	slices.SortFunc(e.adapters, func(a, b adapter.Adapter) int { return cmp.Compare(a.ID(), b.ID()) })
	if e.newExec == nil {
		e.newExec = NewExecutor
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.wait <= 0 {
		e.wait = manifest.DefaultWait
	}
	return e, nil
}

// ErrUnknownTool is returned for a tool identifier no adapter has.
var ErrUnknownTool = errors.New("no such tool")

// checkTools returns an error for any identifier that names no adapter.
func (e *Engine) checkTools(ids []string) error {
	for _, id := range ids {
		if !slices.ContainsFunc(e.adapters, func(a adapter.Adapter) bool { return a.ID() == id }) {
			return fmt.Errorf("%w: %q", ErrUnknownTool, id)
		}
	}
	return nil
}

// tool is one adapter as detected on this computer.
type tool struct {
	a       adapter.Adapter
	profile adapter.Profile
	env     adapter.Env
	det     adapter.Detection
	// reads are the paths the adapter read through env; a preview copies them into its sandbox.
	reads *readLog
	// roots are the places the executor lets this tool write: the detected roots and the places
	// the profile names (rule: "inside a root the tool declares").
	roots map[string]string
}

func (t *tool) id() string { return t.a.ID() }

// detect runs every adapter's Detect. A Detect that fails is an Unknown status.
func (e *Engine) detect(ctx context.Context) ([]*tool, error) {
	var out []*tool
	for _, a := range e.adapters {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		t := &tool{a: a, profile: a.Profile(), reads: &readLog{}}
		t.env = e.envFor(t.profile, t.reads)
		d, err := a.Detect(ctx, t.env)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			d = adapter.Detection{Status: adapter.Unknown, Reason: err.Error()}
		}
		t.det = d
		t.roots = toolRoots(t.profile, d)
		out = append(out, t)
	}
	return out, nil
}

// toolRoots returns the detected roots plus every place the profile names, under names that
// cannot clash with a profile's own.
func toolRoots(p adapter.Profile, d adapter.Detection) map[string]string {
	out := maps.Clone(d.Roots)
	if out == nil {
		out = map[string]string{}
	}
	add := func(name, tmpl string) {
		if v, err := adapter.ExpandPath(tmpl, d.Roots); err == nil {
			out[name] = v
		}
	}
	if p.Skills != nil {
		add("@skills", p.Skills.Dir)
	}
	if p.Commands != nil {
		add("@commands", p.Commands.Dir)
	}
	if p.Agents != nil {
		add("@agents", p.Agents.Dir)
	}
	if p.MCP != nil {
		for i, f := range p.MCP.Files {
			add(fmt.Sprintf("@mcp%d", i), f)
		}
	}
	return out
}

// extraRoots returns the directories outside the home directory that the tools' roots resolve
// to and that exist, in the operating system's form: the executor needs one os.Root for each.
func (e *Engine) extraRoots(tools []*tool) []string {
	var out []string
	for _, t := range tools {
		for _, r := range t.roots {
			if r == "~" || strings.HasPrefix(r, "~/") {
				continue
			}
			native := filepath.Clean(filepath.FromSlash(r))
			if rel, err := filepath.Rel(e.sys.Home, native); err == nil && filepath.IsLocal(rel) {
				continue
			}
			if fi, err := os.Stat(native); err == nil && fi.IsDir() && !slices.Contains(out, native) {
				out = append(out, native)
			}
		}
	}
	slices.Sort(out)
	return out
}

// credentials returns the credential file names of every profile.
func (e *Engine) credentials() []string {
	var out []string
	for _, a := range e.adapters {
		for _, c := range a.Profile().Credentials {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return out
}

// executorOn makes an executor for the real home directory.
func (e *Engine) executorOn(extra []string, journal executor.Journal) (Executor, error) {
	return e.newExec(executor.Roots{Home: e.sys.Home, Extra: extra}, executor.State{Data: e.data, Journal: journal, Now: e.now})
}
