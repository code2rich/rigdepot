// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adaptertest

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
)

// This file is the seam between the suite and the core. The suite drives an executor (it applies,
// observes and reverts operations) and a sandbox (it prepares the temporary home a preview runs
// in). The real ones are internal/executor and internal/sandbox; until they exist, the stand-ins
// in standin.go take their place. Swapping them means changing newExecutor and newSandbox below,
// and nothing else in the suite.

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
	undo   any    // what the executor needs to undo it; the suite does not look inside
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
	// the product's prefix, or outside the home directory, is refused as a defect in the adapter.
	Apply(op adapter.Op, pre observed) (entry, error)
	// Revert undoes a manifest entry. A target changed since it was written is copied aside
	// first (kept reports that); one that is gone is left gone.
	Revert(e entry) (kept bool, err error)
}

// sandbox is what the suite needs of internal/sandbox.
type sandbox interface {
	// Prepare returns a new temporary home that holds what the targets touch in home, at the
	// same relative paths, for a preview to run in.
	Prepare(home string, targets []target) (string, error)
}

// newExecutor makes the executor for one home. state is a directory of the core's own (backups,
// kept copies) outside the home; places are the directories and files the adapter declared (its
// roots and the targets its profile names), and nothing outside them is written. When
// internal/executor lands, this becomes a call of executor.New with the home and these roots.
var newExecutor = func(home, state string, places []string) executor {
	return newStandInExecutor(home, state, places)
}

// newSandbox makes the sandbox; previews are prepared below base. When internal/sandbox lands,
// this becomes a call of sandbox.New.
var newSandbox = func(base func() string) sandbox { return standInSandbox{base: base} }
