// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// Outcome says what Revert found and did.
type Outcome string

const (
	// Removed: the target was as written and is gone now.
	Removed Outcome = "removed"
	// RemovedKept: the target had been changed; a copy is in kept/ and the target is gone.
	RemovedKept Outcome = "removed_kept"
	// AlreadyGone: nothing was at the target any more; only the entry was dropped.
	AlreadyGone Outcome = "already_gone"
)

// Reverted is the result of Revert.
type Reverted struct {
	Outcome Outcome
	// Kept is where the changed target was copied, relative to the local data directory.
	Kept string
	// Leftover is a directory moved aside that could not be removed, or "".
	Leftover string
}

// Revert undoes what a manifest entry records and drops the entry. A target that was changed
// since it was written is copied to kept/ first. Only the target and the empty directories the
// entry created are removed; nothing else.
func (e *Executor) Revert(ctx context.Context, en manifest.Entry) (Reverted, error) {
	if err := ctx.Err(); err != nil {
		return Reverted{}, err
	}
	switch en.Op {
	case manifest.OpTree, manifest.OpFile:
	case manifest.OpKey:
		return e.revertKey(en)
	default:
		return Reverted{}, fmt.Errorf("reverting a %q entry: %w", en.Op, ErrNotImplemented)
	}
	t, err := e.resolve(en.Path)
	if err != nil {
		return Reverted{}, fmt.Errorf("entry %s: %w", en.ID, err)
	}
	if !prefixed(t.rel) {
		return Reverted{}, fmt.Errorf("entry %s: %s does not carry the prefix; refusing to remove it", en.ID, en.Path)
	}
	cur := observe(t, en.Op)
	if cur.State == Unknown {
		return Reverted{}, fmt.Errorf("%s: %w: %v", t.display, ErrUnreadable, cur.Err)
	}
	var res Reverted
	switch {
	case cur.State == Absent:
		res.Outcome = AlreadyGone
	case cur.Digest != en.Digest:
		res.Outcome = RemovedKept
		if res.Kept, err = e.keep(t); err != nil {
			return Reverted{}, fmt.Errorf("keeping the changed %s: %w", t.display, err)
		}
	default:
		res.Outcome = Removed
	}
	in, err := e.journal.Intend(manifest.Intent{Kind: manifest.Remove, Entry: en, Before: before(cur), After: ""})
	if err != nil {
		return Reverted{}, err
	}
	if cur.State == Present {
		if en.Op == manifest.OpFile && !strings.HasPrefix(cur.Digest, "other:") {
			if err := t.root.Remove(filepath.FromSlash(t.rel)); err != nil {
				return Reverted{}, e.forget(in, err)
			}
		} else {
			// Move it aside first: the target is gone in one step, and what remains of a
			// removal that fails halfway has a name of its own.
			old := sibling(t.rel, "old", in.ID)
			if err := renameIn(t.root, t.rel, old); err != nil {
				return Reverted{}, e.forget(in, err)
			}
			if err := removeAllIn(t.root, old); err != nil {
				res.Leftover = e.loc.display(t.idx, old)
			}
		}
	}
	e.removeEmpty(t.root, e.createdRel(t, en.Created))
	if _, err := e.journal.Commit(in); err != nil {
		return Reverted{}, err
	}
	return res, nil
}

func (e *Executor) forget(in manifest.Intent, cause error) error {
	if err := e.journal.Forget(in.ID); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// keep copies what is at a target to kept/<time>/ in the local data directory and returns
// where, relative to the local data directory. A symbolic link is kept as a small text file
// holding where it pointed.
func (e *Executor) keep(t target) (string, error) {
	if e.data.Path() == "" {
		return "", errors.New("there is no local data directory to keep it in")
	}
	if e.keptAbs == "" {
		abs, rel, err := e.data.NewStamped(home.KeptDir, e.now())
		if err != nil {
			return "", err
		}
		e.keptAbs, e.keptRel = abs, rel
	}
	dest := filepath.Join(e.keptAbs, keptPath(t.display))
	fsys := t.root.FS()
	err := fs.WalkDir(fsys, t.rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(dest, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(p, t.rel), "/")))
		switch {
		case d.IsDir():
			return os.MkdirAll(out, home.DirPerm)
		case d.Type().IsRegular():
			data, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(out), home.DirPerm); err != nil {
				return err
			}
			return os.WriteFile(out, data, home.FilePerm)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := t.root.Readlink(filepath.FromSlash(p))
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(out), home.DirPerm); err != nil {
				return err
			}
			return os.WriteFile(out+".symlink", []byte(link+"\n"), home.FilePerm)
		}
		return nil // devices, sockets: nothing to keep
	})
	if err != nil {
		return "", err
	}
	return e.keptRel + "/" + filepath.ToSlash(keptPath(t.display)), nil
}

// Settle puts right what a write or removal interrupted by a crash left behind, then reports
// what is at the target. It is what manifest.Locked.Recover is given to observe with:
//
//	rec, err := locked.Recover(executor.Settle)
//
// An interrupted write leaves its new content under a temporary name (removed) and possibly
// the old content moved aside: if the target is empty, the old content is put back, so that
// the write did not happen; if the new content made it into place, the old one is removed.
// An interrupted removal is finished. Directories the write created are removed again when the
// write did not happen.
func (e *Executor) Settle(in manifest.Intent) manifest.Observation {
	en := in.Entry
	if en.Op == manifest.OpKey {
		return e.settleKey(in)
	}
	if en.Op != manifest.OpTree && en.Op != manifest.OpFile {
		return manifest.Observation{Err: fmt.Errorf("settling a %q intent: %w", en.Op, ErrNotImplemented)}
	}
	t, err := e.resolve(en.Path)
	if err == nil && !prefixed(t.rel) {
		err = fmt.Errorf("%s does not carry the prefix", en.Path)
	}
	if err != nil {
		return manifest.Observation{Err: err}
	}
	old := sibling(t.rel, "old", in.ID)
	_, oldErr := t.root.Lstat(filepath.FromSlash(old))
	hasOld := oldErr == nil
	if in.Kind == manifest.Remove {
		if hasOld {
			removeAllIn(t.root, old)
		}
	} else {
		removeAllIn(t.root, sibling(t.rel, "tmp", in.ID))
		cur := observe(t, en.Op)
		switch {
		case hasOld && cur.State == Absent:
			if err := renameIn(t.root, old, t.rel); err != nil {
				return manifest.Observation{Err: fmt.Errorf("putting %s back: %w", t.display, err)}
			}
		case hasOld && cur.State == Present && cur.Digest == in.After:
			removeAllIn(t.root, old)
		}
	}
	cur := observe(t, en.Op)
	switch cur.State {
	case Unknown:
		return manifest.Observation{Err: cur.Err}
	case Absent:
		if in.Kind == manifest.Remove || in.Before == "" {
			e.removeEmpty(t.root, e.createdRel(t, en.Created))
		}
		return manifest.Observation{}
	}
	return manifest.Observation{Exists: true, Digest: cur.Digest}
}
