// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// Steps of a write at which tests can make it fail or stop.
const (
	stepStaged   = "staged-file" // after each file of the new content was written
	stepMovedOld = "moved-old"   // after what was at the target was moved aside
	stepMovedNew = "moved-new"   // after the new content was moved into place
)

func before(o Observed) string {
	if o.State == Present {
		return o.Digest
	}
	return ""
}

// checkFiles applies the rules for the files of a tree: relative, clean, unique (also when
// case is ignored, as macOS and Windows do), no file where another needs a directory.
func checkFiles(tool Tool, op adapter.WriteTree) error {
	defect := func(problem string) error { return &DefectError{Tool: tool.ID, Target: op.Dir, Problem: problem} }
	seen := map[string]bool{}
	locals := map[string]bool{}
	for _, l := range op.Local {
		if err := checkTreeFile(l); err != nil {
			return defect("local directory: " + err.Error())
		}
		key := strings.ToLower(l)
		if locals[key] {
			return defect(fmt.Sprintf("local directory %q is listed twice (ignoring case)", l))
		}
		locals[key] = true
	}
	for _, f := range op.Files {
		if locals[strings.ToLower(f.Path)] {
			return defect(fmt.Sprintf("%q is both a file and a local directory", f.Path))
		}
		if err := checkTreeFile(f.Path); err != nil {
			return defect(err.Error())
		}
		key := strings.ToLower(f.Path)
		if seen[key] {
			return defect(fmt.Sprintf("file %q is listed twice (ignoring case)", f.Path))
		}
		seen[key] = true
	}
	for _, f := range op.Files {
		for dir := path.Dir(f.Path); dir != "."; dir = path.Dir(dir) {
			if seen[strings.ToLower(dir)] {
				return defect(fmt.Sprintf("%q is both a file and the directory of %q", dir, f.Path))
			}
		}
	}
	return nil
}

// write is one write in progress, with everything needed to take it back.
type write struct {
	e       *Executor
	t       target
	in      manifest.Intent
	missing []string // parents to create, relative to the root, outermost first
	stage   string
	local   []string // a tree's local directories: left out of the digest when verifying
	old     string   // set once what was at the target has been moved aside
	movedIn bool     // the new content is at the target
}

// undo takes a failed write back: the new content out, the old content back, the staging area
// and the parents it created removed, the intent forgotten. If the old content cannot be put
// back, the intent stays, so that Settle tries again on the next run.
func (w *write) undo(cause error) error {
	root := w.t.root
	if w.movedIn && w.old == "" && w.in.Before != "" {
		// A file was replaced by the rename itself: the old content is gone and the new one is
		// complete. The intent stays; recovery finds the target in its final state.
		return fmt.Errorf("%w (the new content of %s is already in place)", cause, w.t.display)
	}
	if w.movedIn {
		if err := renameIn(root, w.t.rel, w.stage); err != nil {
			return errors.Join(cause, fmt.Errorf("could not take the new content out of %s: %w", w.t.display, err))
		}
	}
	if w.old != "" {
		if err := renameIn(root, w.old, w.t.rel); err != nil {
			return errors.Join(cause, fmt.Errorf("could not put %s back; it is at %s: %w", w.t.display, w.old, err))
		}
	}
	removeAllIn(root, w.stage)
	w.e.removeEmpty(root, w.missing)
	if err := w.e.journal.Forget(w.in.ID); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// begin does what every write does first: check the state, announce the write, keep the old
// content when asked, create the parents.
func (e *Executor) begin(t target, entry manifest.Entry, cur Observed, opt Options) (*write, Applied, error) {
	missing, err := missingParents(t)
	if err != nil {
		return nil, Applied{}, fmt.Errorf("%s: %w", t.display, err)
	}
	for _, dir := range missing {
		entry.Created = append(entry.Created, e.loc.display(t.idx, dir))
	}
	in, err := e.journal.Intend(manifest.Intent{Kind: manifest.Write, Entry: entry, Before: before(cur), After: entry.Digest})
	if err != nil {
		return nil, Applied{}, err
	}
	w := &write{e: e, t: t, in: in, missing: missing, stage: sibling(t.rel, "tmp", in.ID)}
	var res Applied
	if opt.Keep && cur.State == Present {
		if res.Kept, err = e.keep(t); err != nil {
			return nil, Applied{}, w.undo(fmt.Errorf("keeping %s before replacing it: %w", t.display, err))
		}
	}
	if _, err := e.makeParents(t, missing); err != nil {
		return nil, Applied{}, w.undo(err)
	}
	return w, res, nil
}

// finish moves the staged content into place, moving what is there aside first when replace
// says so, then verifies and records.
func (w *write) finish(op manifest.Op, replace bool, res Applied) (Applied, error) {
	root := w.t.root
	if replace {
		old := sibling(w.t.rel, "old", w.in.ID)
		if err := renameIn(root, w.t.rel, old); err != nil {
			return Applied{}, w.undo(err)
		}
		w.old = old
		if err := w.e.step(stepMovedOld); err != nil {
			return Applied{}, w.undo(err)
		}
	}
	if err := renameIn(root, w.stage, w.t.rel); err != nil {
		return Applied{}, w.undo(err)
	}
	w.movedIn = true
	if err := w.e.step(stepMovedNew); err != nil {
		return Applied{}, w.undo(err)
	}
	if w.old != "" {
		if err := removeAllIn(root, w.old); err != nil {
			res.Leftover = w.e.loc.display(w.t.idx, w.old)
		}
	}
	if got := observe(w.t, op, w.local...); got.State != Present || got.Digest != w.in.After {
		return Applied{}, fmt.Errorf("%s: %w", w.t.display, ErrVerify)
	}
	recorded, err := w.e.journal.Commit(w.in)
	if err != nil {
		return Applied{}, err
	}
	res.Entry = recorded
	return res, nil
}

func (e *Executor) applyTree(tool Tool, op adapter.WriteTree, pre Observed, opt Options) (Applied, error) {
	t, err := e.checkTarget(tool, op.Resource, op.Dir)
	if err != nil {
		return Applied{}, err
	}
	if err := checkFiles(tool, op); err != nil {
		return Applied{}, err
	}
	cur := observe(t, manifest.OpTree, op.Local...)
	if err := checkPre(pre, cur); err != nil {
		return Applied{}, fmt.Errorf("%s: %w", t.display, err)
	}
	entry := e.newEntry(tool, op.Resource, manifest.OpTree, t, filesDigest(op.Files, op.Local...))
	entry.Local = slices.Clone(op.Local)
	w, res, err := e.begin(t, entry, cur, opt)
	if err != nil {
		return Applied{}, err
	}
	w.local = op.Local
	if err := e.writeStage(w, tool, op); err != nil {
		return Applied{}, w.undo(err)
	}
	if cur.State == Present {
		if err := carryLocal(t.root, t.rel, w.stage, op.Local); err != nil {
			return Applied{}, w.undo(fmt.Errorf("keeping the local directories of %s: %w", t.display, err))
		}
	}
	return w.finish(manifest.OpTree, cur.State == Present, res)
}

// carryLocal puts what is in the local directories of the directory being replaced (from) into
// the new content (to), in place of whatever the new content has there: what the person's
// tools wrote into them belongs to the person. The old directory stays as it is until the new
// one is in place, so a failure loses nothing.
func carryLocal(root *os.Root, from, to string, local []string) error {
	for _, l := range local {
		src := from + "/" + l
		fi, err := root.Lstat(filepath.FromSlash(src))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		dst := to + "/" + l
		if err := removeAllIn(root, dst); err != nil {
			return err
		}
		if !fi.IsDir() {
			// Something else where a directory was meant: carried as it is.
			if err := copyEntry(root, src, dst, fi); err != nil {
				return err
			}
			continue
		}
		err = fs.WalkDir(root.FS(), src, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			out := dst + strings.TrimPrefix(p, src)
			info, err := d.Info()
			if err != nil {
				return err
			}
			return copyEntry(root, p, out, info)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// copyEntry copies one directory entry inside root: a directory is made, a file copied with its
// permissions, a symbolic link made again pointing where it pointed. Anything else is left out.
func copyEntry(root *os.Root, src, dst string, fi fs.FileInfo) error {
	name := filepath.FromSlash(dst)
	if dir := path.Dir(dst); dir != "." {
		if err := root.MkdirAll(filepath.FromSlash(dir), 0o755); err != nil {
			return err
		}
	}
	switch {
	case fi.IsDir():
		return root.MkdirAll(name, fi.Mode().Perm()|0o700)
	case fi.Mode().IsRegular():
		data, err := root.ReadFile(filepath.FromSlash(src))
		if err != nil {
			return err
		}
		return writeNew(root, dst, data, fi.Mode().Perm())
	case fi.Mode()&fs.ModeSymlink != 0:
		link, err := root.Readlink(filepath.FromSlash(src))
		if err != nil {
			return err
		}
		return root.Symlink(link, name)
	}
	return nil
}

// writeStage writes the new directory next to the target, every file flushed to disk.
func (e *Executor) writeStage(w *write, tool Tool, op adapter.WriteTree) error {
	root := w.t.root
	if err := root.Mkdir(filepath.FromSlash(w.stage), 0o755); err != nil {
		return err
	}
	for _, f := range op.Files {
		name := w.stage + "/" + f.Path
		if dir := path.Dir(name); dir != w.stage {
			if err := root.MkdirAll(filepath.FromSlash(dir), 0o755); err != nil {
				return err
			}
		}
		perm := fs.FileMode(0o644)
		if f.Exec {
			perm = 0o755
		}
		if err := writeNew(root, name, f.Data, perm); err != nil {
			return err
		}
		if err := e.step(stepStaged); err != nil {
			return err
		}
	}
	marker, err := json.MarshalIndent(map[string]any{
		"note":     brand.ManagedMarker,
		"tool":     tool.ID,
		"resource": op.Resource.String(),
		"digest":   w.in.After,
	}, "", "  ")
	if err != nil {
		return err
	}
	return writeNew(root, w.stage+"/"+MarkerFile, append(marker, '\n'), 0o644)
}

// writeNew creates a file that must not exist yet and flushes it to disk.
func writeNew(root *os.Root, name string, data []byte, perm fs.FileMode) error {
	f, err := root.OpenFile(filepath.FromSlash(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (e *Executor) applyFile(tool Tool, op adapter.WriteFile, pre Observed, opt Options) (Applied, error) {
	t, err := e.checkTarget(tool, op.Resource, op.Path)
	if err != nil {
		return Applied{}, err
	}
	cur := observe(t, manifest.OpFile)
	if err := checkPre(pre, cur); err != nil {
		return Applied{}, fmt.Errorf("%s: %w", t.display, err)
	}
	entry := e.newEntry(tool, op.Resource, manifest.OpFile, t, fileDigest(op.Data))
	w, res, err := e.begin(t, entry, cur, opt)
	if err != nil {
		return Applied{}, err
	}
	if err := writeNew(t.root, w.stage, op.Data, 0o644); err != nil {
		return Applied{}, w.undo(err)
	}
	if err := e.step(stepStaged); err != nil {
		return Applied{}, w.undo(err)
	}
	// A regular file is replaced by the rename itself, atomically. Anything else at the target
	// (a directory, a link) is moved aside first, the way a directory is.
	replace := cur.State == Present && strings.HasPrefix(cur.Digest, "other:")
	return w.finish(manifest.OpFile, replace, res)
}
