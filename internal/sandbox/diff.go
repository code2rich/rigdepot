// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// MaxContent is the largest file whose content a snapshot keeps for the line diff. Larger files
// are compared by digest only.
const MaxContent = 1 << 20

// Kind is what a path is.
type Kind string

const (
	File    Kind = "file"
	Dir     Kind = "dir"
	Symlink Kind = "symlink"
)

// Action is what happened to a path.
type Action string

const (
	Created  Action = "created"
	Modified Action = "modified"
	Removed  Action = "removed"
)

// Change is one path that differs between two snapshots, translated back to the real computer.
type Change struct {
	// Path is the path as written in operations: "~/…" or absolute, with forward slashes.
	Path string
	// Real is the path on the real computer, in the operating system's form.
	Real   string
	Action Action
	// Kind is what the path is after the change, or was before a removal.
	Kind Kind
	// Before and After are the content of a file before and after, when it is at most
	// MaxContent bytes; nil otherwise and for directories. For a symbolic link they hold the
	// link's target.
	Before, After []byte
	// BeforeDigest and AfterDigest are "sha256:<hex>" of a file's content, or "".
	BeforeDigest, AfterDigest string
	// Withheld means the path is, or lies inside, a credential file that was not copied: the
	// preview did not see what is really there.
	Withheld bool
}

// Snapshot is the state of a sandbox at one moment: every path in it, except DataDir.
type Snapshot map[string]state

type state struct {
	kind   Kind
	digest string
	exec   bool
	data   []byte // when at most MaxContent, or the target of a link
}

// Snapshot records every file, directory and link in the sandbox.
func (b *Sandbox) Snapshot() (Snapshot, error) {
	snap := Snapshot{}
	for _, top := range []string{b.home, b.ext} {
		err := filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p == top {
				return nil
			}
			var st state
			switch {
			case d.Type()&fs.ModeSymlink != 0:
				target, err := os.Readlink(p)
				if err != nil {
					return err
				}
				st = state{kind: Symlink, data: []byte(target), digest: digest([]byte(target))}
			case d.IsDir():
				st = state{kind: Dir}
			default:
				info, err := d.Info()
				if err != nil {
					return err
				}
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				st = state{kind: File, digest: digest(data), exec: runtime.GOOS != "windows" && info.Mode()&0o100 != 0}
				if len(data) <= MaxContent {
					st.data = data
				}
			}
			snap[p] = st
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("sandbox: snapshot: %w", err)
		}
	}
	return snap, nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Diff compares two snapshots of this sandbox and returns the differences, sorted by path, with
// paths translated back to the real computer. A file whose content or executable bit changed is
// modified; a path that changed kind is removed and created.
func (b *Sandbox) Diff(before, after Snapshot) []Change {
	var out []Change
	add := func(p string, action Action, old, cur *state) {
		target, real, ok := b.Real(p)
		if !ok {
			return
		}
		c := Change{Path: target, Real: real, Action: action, Withheld: b.isWithheld(target)}
		if old != nil {
			c.Kind, c.Before, c.BeforeDigest = old.kind, old.data, old.digest
		}
		if cur != nil {
			c.Kind, c.After, c.AfterDigest = cur.kind, cur.data, cur.digest
		}
		out = append(out, c)
	}
	for p, old := range before {
		cur, ok := after[p]
		switch {
		case !ok:
			add(p, Removed, &old, nil)
		case cur.kind != old.kind:
			add(p, Removed, &old, nil)
			add(p, Created, nil, &cur)
		case cur.digest != old.digest || cur.exec != old.exec:
			add(p, Modified, &old, &cur)
		}
	}
	for p, cur := range after {
		if _, ok := before[p]; !ok {
			add(p, Created, nil, &cur)
		}
	}
	slices.SortStableFunc(out, func(x, y Change) int {
		if c := strings.Compare(x.Path, y.Path); c != 0 {
			return c
		}
		return strings.Compare(order(x.Action), order(y.Action))
	})
	return out
}

// order puts a removal before a creation of the same path.
func order(a Action) string {
	if a == Removed {
		return "0"
	}
	return "1"
}

func (b *Sandbox) isWithheld(target string) bool {
	for w := range b.withheld {
		if target == w || strings.HasPrefix(target, w+"/") {
			return true
		}
	}
	return false
}
