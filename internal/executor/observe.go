// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package executor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing/fstest"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/bundle"
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

// Expect returns the digest the target of op will have once op is applied, in the form Observe
// reports and the manifest records. It writes nothing, so that the sync engine can tell "already
// as wanted" from "has to change" while it plans.
//
// For a SetKey it is the SHA-256 of the value serialised as canonical JSON (keys sorted, no HTML
// escaping), the digest the manifest records for a key. An Exec has no digest.
func Expect(op adapter.Op) (string, error) {
	switch op := op.(type) {
	case adapter.WriteTree:
		return filesDigest(op.Files), nil
	case adapter.WriteFile:
		return fileDigest(op.Data), nil
	case adapter.SetKey:
		return keyDigest(op.Value)
	case adapter.Exec:
		return "", fmt.Errorf("running %v: an Exec operation has no digest", op.Argv)
	}
	return "", fmt.Errorf("unknown operation %T", op)
}

// keyDigest is the digest of a configuration value: canonical JSON, then SHA-256.
func keyDigest(v any) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return fileDigest(bytes.TrimSuffix(b.Bytes(), []byte("\n"))), nil
}

// Observe looks at a target without changing anything.
func (e *Executor) Observe(t Target) Observed {
	switch t.Op {
	case manifest.OpTree, manifest.OpFile:
	case manifest.OpKey:
		return e.observeKey(t)
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

// emptyDigest is the digest of a directory without files: the SHA-256 of an empty listing.
// bundle.Digest refuses to digest nothing; a directory someone emptied still has to read as
// present and changed.
var emptyDigest = func() string {
	sum := sha256.Sum256(nil)
	return "sha256:" + hex.EncodeToString(sum[:])
}()

// filesDigest is the content digest of the files a WriteTree writes. It is bundle.Digest, the
// digest package repoformat records for a resource, so that the digest of a skill in the
// repository and the digest of the directory written from it are the same value.
func filesDigest(files []adapter.File) string {
	fsys := make(fstest.MapFS, len(files))
	for _, f := range files {
		fsys[f.Path] = &fstest.MapFile{Data: f.Data}
	}
	return digestOf(fsys)
}

func digestOf(fsys fs.FS) string {
	d, err := bundle.Digest(fsys)
	if errors.Is(err, bundle.ErrEmpty) {
		return emptyDigest
	}
	if err != nil {
		// Only an unreadable entry gets here, and the callers have ruled that out; a value
		// that equals no digest keeps it from passing for anything.
		return "other:" + err.Error()
	}
	return string(d)
}

// treeDigest is the content digest of a directory on disk, computed by bundle.Digest. The
// marker at its top is left out. Anything that is neither a file nor a directory (a symbolic
// link someone put there) makes the digest one that equals no content digest, so that it shows
// up as a change.
func treeDigest(root *os.Root, rel string) (string, error) {
	fsys, err := fs.Sub(root.FS(), rel)
	if err != nil {
		return "", err
	}
	other := ""
	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !d.Type().IsRegular() && other == "" {
			other = "other:contains-" + d.Type().String() + ":" + p
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if other != "" {
		return other, nil
	}
	return digestOf(fsys), nil
}
