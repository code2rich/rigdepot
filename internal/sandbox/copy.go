// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Copy copies the targets that exist into the sandbox, at the same relative paths, and nothing
// else. For every target, the directories that lead to it are created in the sandbox as far as
// they exist on the real computer, so that the diff does not report them as new; their other
// content is not copied.
//
// A target that is a directory is copied with everything in it. Symbolic links are followed for
// the target itself and for the directories leading to it, because a tool's directory is often a
// link into a dotfiles repository; inside a copied directory, a link to a file is copied as that
// file and a link to a directory is left out. A credential file is never copied.
//
// A target that cannot be read is an error, not a missing file: the preview would otherwise show
// the creation of something that is already there.
func (b *Sandbox) Copy(targets []string) error {
	for _, t := range targets {
		loc, err := b.locate(t)
		if err != nil {
			return err
		}
		if err := b.mirrorParents(loc); err != nil {
			return err
		}
		if b.isCredential(filepath.Base(loc.real)) {
			b.withheld[loc.target] = true
			continue
		}
		info, err := os.Stat(loc.real)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("sandbox: reading %s: %w", loc.target, err)
		}
		if err := b.copyEntry(loc.real, loc.inBox, loc.target, info); err != nil {
			return err
		}
	}
	return nil
}

// mirrorParents creates in the sandbox the directories between the top (the home directory or a
// volume root) and the target that exist on the real computer.
func (b *Sandbox) mirrorParents(loc location) error {
	if err := os.MkdirAll(loc.topBox, 0o700); err != nil {
		return err
	}
	rel, err := filepath.Rel(loc.top, filepath.Dir(loc.real))
	if err != nil || rel == "." {
		return err
	}
	real, inBox := loc.top, loc.topBox
	for _, elem := range strings.Split(rel, string(filepath.Separator)) {
		real, inBox = filepath.Join(real, elem), filepath.Join(inBox, elem)
		info, err := os.Stat(real)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("sandbox: reading %s: %w", real, err)
		}
		if !info.IsDir() {
			return nil
		}
		if err := os.Mkdir(inBox, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	return nil
}

// copyEntry copies one file or directory tree. target is the path as written, for messages and
// for the list of withheld files.
func (b *Sandbox) copyEntry(real, inBox, target string, info fs.FileInfo) error {
	switch {
	case info.Mode().IsRegular():
		return copyFile(real, inBox, info)
	case info.IsDir():
		if err := os.Mkdir(inBox, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		entries, err := os.ReadDir(real)
		if err != nil {
			return fmt.Errorf("sandbox: reading %s: %w", target, err)
		}
		for _, e := range entries {
			name := e.Name()
			childTarget := target + "/" + name
			if b.isCredential(name) {
				b.withheld[childTarget] = true
				continue
			}
			childReal := filepath.Join(real, name)
			child, err := os.Lstat(childReal)
			if err != nil {
				return fmt.Errorf("sandbox: reading %s: %w", childTarget, err)
			}
			if child.Mode()&fs.ModeSymlink != 0 {
				child, err = os.Stat(childReal)
				if errors.Is(err, fs.ErrNotExist) {
					continue // a dangling link
				}
				if err != nil {
					return fmt.Errorf("sandbox: reading %s: %w", childTarget, err)
				}
				if !child.Mode().IsRegular() {
					continue
				}
			}
			if !child.Mode().IsRegular() && !child.IsDir() {
				continue // sockets, devices, pipes: nothing a tool configuration consists of
			}
			if err := b.copyEntry(childReal, filepath.Join(inBox, name), childTarget, child); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

// copyFile copies a regular file, keeping its permission bits and making sure the owner can
// write the copy, since the executor has to be able to change it.
func copyFile(real, inBox string, info fs.FileInfo) error {
	data, err := os.ReadFile(real)
	if err != nil {
		return fmt.Errorf("sandbox: reading %s: %w", real, err)
	}
	perm := info.Mode().Perm() | 0o600
	if err := os.WriteFile(inBox, data, perm); err != nil {
		return err
	}
	// WriteFile's mode is filtered by the umask; the copy keeps the original bits.
	return os.Chmod(inBox, perm)
}
