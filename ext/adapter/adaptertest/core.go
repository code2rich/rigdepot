// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adaptertest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing/fstest"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/bundle"
	coreexec "github.com/code2rich/rigdepot/internal/executor"
	"github.com/code2rich/rigdepot/internal/home"
	coremanifest "github.com/code2rich/rigdepot/internal/manifest"
	coresandbox "github.com/code2rich/rigdepot/internal/sandbox"
)

// This file is the seam between the suite and the core. The suite drives an executor (it applies,
// observes and reverts operations) and a sandbox (it prepares the temporary home a preview runs
// in) through the two small interfaces below; newExecutor and newSandbox make them from
// internal/executor and internal/sandbox. The suite's own tests replace newExecutor to break the
// core on purpose.

// target identifies what an operation writes: a tree, a file, a key in a file, or a command.
type target struct {
	kind   string // "tree", "file", "key" or "exec"
	path   string // in the package form of ext/adapter: "~/…" or absolute, forward slashes
	key    []string
	format adapter.Format
}

func (t target) String() string {
	switch t.kind {
	case "key":
		return "key " + t.path + " " + strings.Join(t.key, ".")
	case "exec":
		return "exec " + t.path
	}
	return t.kind + " " + t.path
}

// id is a comparable form of the target, for maps.
func (t target) id() string {
	k, _ := json.Marshal(t.key)
	return t.kind + "\x00" + t.path + "\x00" + string(k)
}

// targetOf returns what op writes.
func targetOf(op adapter.Op) target {
	switch o := op.(type) {
	case adapter.WriteTree:
		return target{kind: "tree", path: o.Dir}
	case adapter.WriteFile:
		return target{kind: "file", path: o.Path}
	case adapter.SetKey:
		return target{kind: "key", path: o.File, key: o.Path, format: o.Format}
	case adapter.Exec:
		return target{kind: "exec", path: strings.Join(o.Argv, " ")}
	}
	return target{kind: fmt.Sprintf("%T", op)}
}

// state is what a target looks like on disk.
type state int

const (
	absent state = iota
	present
	unreadable // it is there and could not be read or parsed: unknown, never "absent"
)

type observed struct {
	state  state
	digest string // when present
	reason string // when unreadable
}

// entry is a manifest entry: something the core wrote and can undo.
type entry struct {
	target target
	ref    resource.Ref
	digest string // what the target looked like right after it was written
	undo   any    // what the executor needs to undo it: a coremanifest.Entry, or an interimUndo
}

// executor is what the suite needs of internal/executor. All of it works inside one home
// directory, given when the executor is made.
type executor interface {
	// Expect returns the digest the target of op will have once op is applied, so that the
	// core can tell "already as wanted" from "has to change" without writing.
	Expect(op adapter.Op) (string, error)
	// Observe looks at a target without changing anything.
	Observe(t target) observed
	// Apply carries out op. pre is what Observe returned when the plan was made; if the target
	// no longer looks like that, nothing is written and an error is returned. A target without
	// the product's prefix, or outside the places the tool declared, is refused as a defect in
	// the adapter.
	Apply(op adapter.Op, pre observed) (entry, error)
	// Revert undoes a manifest entry. A target changed since it was written is copied aside
	// first (kept reports that); one that is gone is left gone.
	Revert(e entry) (kept bool, err error)
	// Close releases what the executor holds open.
	Close() error
}

// sandbox is what the suite needs of internal/sandbox.
type sandbox interface {
	// Prepare returns a new temporary home that holds what the targets touch in home, at the
	// same relative paths, for a preview to run in.
	Prepare(home string, targets []target) (string, error)
}

// coreTool is what the core knows about the tool it writes for.
type coreTool struct {
	id     string
	roots  map[string]string // adapter.Detection.Roots
	places []string          // declaredPlaces: the roots and the places the profile names
}

// newExecutor makes the executor for one home. state is the core's local data directory (kept
// copies) outside the home.
var newExecutor = func(home, state string, tool coreTool) (executor, error) {
	return newCoreExecutor(home, state, tool)
}

// newSandbox makes the sandbox; previews are prepared below base. credentials are the file
// names the profile lists, which a preview never copies.
var newSandbox = func(base func() string, credentials []string) sandbox {
	return coreSandbox{base: base, credentials: credentials}
}

// coreExecutor is internal/executor behind the suite's interface.
type coreExecutor struct {
	ex   *coreexec.Executor
	tool coreexec.Tool
	// realKeys says whether internal/executor sets keys. Until it does, keys go to the interim
	// handler in interimkeys.go. TODO(M0-20): remove the field and the interim handler.
	realKeys bool
	keys     *interimKeys
}

func newCoreExecutor(homeDir, stateDir string, tool coreTool) (*coreExecutor, error) {
	data, err := home.At(stateDir)
	if err != nil {
		return nil, err
	}
	roots := coreexec.Roots{Home: homeDir}
	toolRoots := make(map[string]string, len(tool.roots)+len(tool.places))
	for name, r := range tool.roots {
		toolRoots[name] = r
		if native := filepath.FromSlash(r); filepath.IsAbs(native) {
			if rel, err := filepath.Rel(homeDir, native); err != nil || !filepath.IsLocal(rel) {
				if _, err := os.Stat(native); err == nil {
					roots.Extra = append(roots.Extra, native)
				}
			}
		}
	}
	sort.Strings(roots.Extra)
	// What the profile names counts as declared too: Codex keeps its skills in ~/.agents/skills,
	// outside its configuration root (DEVIATIONS: "目标在声明的根之内").
	for i, p := range tool.places {
		toolRoots[fmt.Sprintf("place %d", i)] = p
	}
	ex, err := coreexec.New(roots, coreexec.State{Data: data})
	if err != nil {
		return nil, err
	}
	c := &coreExecutor{
		ex:   ex,
		tool: coreexec.Tool{ID: tool.id, Roots: toolRoots},
		keys: &interimKeys{home: homeDir, places: tool.places},
	}
	probe := ex.Observe(coreexec.Target{Op: coremanifest.OpKey, Path: "~/" + brand.Prefix + "probe.json", Format: adapter.JSON, Key: []string{"probe"}})
	c.realKeys = !errors.Is(probe.Err, coreexec.ErrNotImplemented)
	return c, nil
}

func (c *coreExecutor) Close() error { return c.ex.Close() }

func execTarget(t target) coreexec.Target {
	switch t.kind {
	case "tree":
		return coreexec.Target{Op: coremanifest.OpTree, Path: t.path}
	case "file":
		return coreexec.Target{Op: coremanifest.OpFile, Path: t.path}
	case "key":
		return coreexec.Target{Op: coremanifest.OpKey, Path: t.path, Format: t.format, Key: t.key}
	}
	return coreexec.Target{Op: coremanifest.OpExec, Path: t.path}
}

func fromExec(o coreexec.Observed) observed {
	switch o.State {
	case coreexec.Absent:
		return observed{state: absent}
	case coreexec.Present:
		return observed{state: present, digest: o.Digest}
	}
	reason := "unknown"
	if o.Err != nil {
		reason = o.Err.Error()
	}
	return observed{state: unreadable, reason: reason}
}

func toExec(o observed) coreexec.Observed {
	switch o.state {
	case absent:
		return coreexec.Observed{State: coreexec.Absent}
	case present:
		return coreexec.Observed{State: coreexec.Present, Digest: o.digest}
	}
	return coreexec.Observed{State: coreexec.Unknown, Err: errors.New(o.reason)}
}

// Expect computes the digest the executor records for op: the content digest of a tree
// (package bundle, which leaves the marker file out) and the sha256 of a file.
func (c *coreExecutor) Expect(op adapter.Op) (string, error) {
	switch o := op.(type) {
	case adapter.WriteTree:
		fsys := fstest.MapFS{}
		for _, f := range o.Files {
			fsys[f.Path] = &fstest.MapFile{Data: f.Data}
		}
		d, err := bundle.Digest(fsys)
		return string(d), err
	case adapter.WriteFile:
		sum := sha256.Sum256(o.Data)
		return "sha256:" + hex.EncodeToString(sum[:]), nil
	case adapter.SetKey:
		if c.realKeys {
			// TODO(M0-20): take the digest of a key the way internal/executor records it.
			return "", errors.New("internal/executor sets keys now: the suite has to compute their digest the way it does (TODO(M0-20) in ext/adapter/adaptertest/core.go)")
		}
		return c.keys.expect(o)
	}
	return "", fmt.Errorf("the suite has no digest for %T", op)
}

func (c *coreExecutor) Observe(t target) observed {
	switch {
	case t.kind == "exec":
		return observed{state: absent}
	case t.kind == "key" && !c.realKeys:
		return c.keys.observe(t)
	}
	return fromExec(c.ex.Observe(execTarget(t)))
}

func (c *coreExecutor) Apply(op adapter.Op, pre observed) (entry, error) {
	t := targetOf(op)
	switch o := op.(type) {
	case adapter.Exec:
		return entry{}, errors.New("tests do not run the tool's own commands")
	case adapter.SetKey:
		if !c.realKeys {
			return c.keys.apply(o, pre)
		}
	}
	res, err := c.ex.Apply(context.Background(), c.tool, op, toExec(pre), coreexec.Options{})
	if err != nil {
		return entry{}, err
	}
	return entry{target: t, ref: op.Ref(), digest: res.Entry.Digest, undo: res.Entry}, nil
}

func (c *coreExecutor) Revert(e entry) (bool, error) {
	switch u := e.undo.(type) {
	case coremanifest.Entry:
		res, err := c.ex.Revert(context.Background(), u)
		return res.Outcome == coreexec.RemovedKept, err
	case interimUndo:
		return c.keys.revert(e, u)
	}
	return false, fmt.Errorf("%s: the entry records nothing to undo it with", e.target)
}

// coreSandbox is internal/sandbox behind the suite's interface.
type coreSandbox struct {
	base        func() string
	credentials []string
}

// Prepare copies what the targets touch into a new sandbox and returns its home directory. The
// sandbox is not closed: it lies in a directory the test removes, and the suite works in it
// after Prepare returns.
func (s coreSandbox) Prepare(homeDir string, targets []target) (string, error) {
	box, err := coresandbox.New(coresandbox.Options{TmpDir: s.base(), Home: homeDir, Credentials: s.credentials})
	if err != nil {
		return "", err
	}
	var paths []string
	for _, t := range targets {
		if t.kind != "exec" {
			paths = append(paths, t.path)
		}
	}
	if err := box.Copy(paths); err != nil {
		return "", err
	}
	return box.Home(), nil
}

// execMarkerFile is the file the executor puts in every directory it writes. It is not part of
// the directory's digest.
const execMarkerFile = coreexec.MarkerFile
