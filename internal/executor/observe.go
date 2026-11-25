// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// Presence says whether something is at a target. The zero value is Unknown, so that a value
// nobody filled in is never taken for "absent".
type Presence int

const (
	// Unknown: the target could not be looked at. Nothing is done to it.
	Unknown Presence = iota
	// Absent: nothing is there.
	Absent
	// Present: something is there; Observed.Digest says what.
	Present
)

func (s Presence) String() string {
	switch s {
	case Absent:
		return "absent"
	case Present:
		return "present"
	}
	return "unknown"
}

// Observed is what a target looks like.
type Observed struct {
	State Presence
	// Digest is set when State is Present. For a target of the expected type it is
	// "sha256:<hex>", in the same form as the manifest; something of another type (a file
	// where a directory is expected, a symbolic link) gets a digest that never equals one.
	Digest string
	// Err is set when State is Unknown.
	Err error
}

// Same reports whether two observations describe the same thing at the target.
func (o Observed) Same(p Observed) bool {
	return o.State == p.State && (o.State != Present || o.Digest == p.Digest)
}

// Target names what Observe looks at.
type Target struct {
	Op   manifest.Op
	Path string
	// Format and Key are for OpKey.
	Format adapter.Format
	Key    []string
}

// TargetOf returns the target of an operation.
func TargetOf(op adapter.Op) Target {
	switch op := op.(type) {
	case adapter.WriteTree:
		return Target{Op: manifest.OpTree, Path: op.Dir}
	case adapter.WriteFile:
		return Target{Op: manifest.OpFile, Path: op.Path}
	case adapter.SetKey:
		return Target{Op: manifest.OpKey, Path: op.File, Format: op.Format, Key: op.Path}
	case adapter.Exec:
		return Target{Op: manifest.OpExec}
	}
	return Target{}
}

// TargetOfEntry returns the target a manifest entry records.
func TargetOfEntry(en manifest.Entry) Target {
	return Target{Op: en.Op, Path: en.Path, Format: adapter.Format(en.Format), Key: en.Key}
}

// Observe looks at a target without changing anything.
func (e *Executor) Observe(t Target) Observed {
	switch t.Op {
	case manifest.OpTree, manifest.OpFile:
	default:
		return Observed{Err: fmt.Errorf("observing a %q target: %w", t.Op, ErrNotImplemented)}
	}
	r, err := e.resolve(t.Path)
	if err != nil {
		return Observed{Err: err}
	}
	return observe(r, t.Op)
}

func observe(t target, op manifest.Op) Observed {
	name := filepath.FromSlash(t.rel)
	fi, err := t.root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return Observed{State: Absent}
	}
	if err != nil {
		return Observed{Err: err}
	}
	switch {
	case op == manifest.OpTree && fi.IsDir():
		d, err := treeDigest(t.root, t.rel)
		if err != nil {
			return Observed{Err: err}
		}
		return Observed{State: Present, Digest: d}
	case op == manifest.OpFile && fi.Mode().IsRegular():
		data, err := t.root.ReadFile(name)
		if err != nil {
			return Observed{Err: err}
		}
		return Observed{State: Present, Digest: fileDigest(data)}
	}
	return Observed{State: Present, Digest: "other:" + fi.Mode().Type().String()}
}

func fileDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// listed is one file of a content digest: its path inside the tree and the line it adds.
type listed struct {
	path, line string
}

// digestOfListing is the content digest of package repoformat: one line "<hex>  <path>" per
// file, sorted by path as bytes, and the sha256 of that text. It is what this shell command
// prints, run inside the directory:
//
//	find . -type f | LC_ALL=C sort | sed 's|^\./||' | xargs sha256sum | sha256sum
//
// TODO(M0-10): use the digest function of internal/repoformat once it is merged.
func digestOfListing(files []listed) string {
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	h := sha256.New()
	for _, f := range files {
		io.WriteString(h, f.line)
		io.WriteString(h, "\n")
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func fileLine(data []byte, p string) listed {
	sum := sha256.Sum256(data)
	return listed{path: p, line: hex.EncodeToString(sum[:]) + "  " + p}
}

// filesDigest is the content digest of the files an operation writes.
func filesDigest(files []adapter.File) string {
	lines := make([]listed, 0, len(files))
	for _, f := range files {
		lines = append(lines, fileLine(f.Data, f.Path))
	}
	return digestOfListing(lines)
}

// treeDigest is the content digest of a directory on disk. The marker at its top is left out.
// Anything that is neither a file nor a directory (a symbolic link someone put there) adds a
// line no file can produce, so that it shows up as a change.
func treeDigest(root *os.Root, rel string) (string, error) {
	var lines []listed
	fsys := root.FS()
	err := fs.WalkDir(fsys, rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		inside := strings.TrimPrefix(strings.TrimPrefix(p, rel), "/")
		switch {
		case d.IsDir():
			return nil
		case inside == MarkerFile:
			return nil
		case d.Type().IsRegular():
			data, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			lines = append(lines, fileLine(data, inside))
		default:
			lines = append(lines, listed{path: inside, line: "other:" + d.Type().String() + "  " + inside})
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return digestOfListing(lines), nil
}
