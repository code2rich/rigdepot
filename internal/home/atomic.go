// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package home

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFileAtomic replaces the file at path with data, so that a reader sees either the old
// content or the new one and never a part of either: the data goes to a temporary file in the
// same directory, is flushed to disk, and the temporary file is renamed over the target. A
// failure at any step leaves the old file as it was and removes the temporary one.
//
// It is meant for the files of the local data directory (manifest, state). The target's
// directory must exist.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) (err error) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(perm); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = Rename(name, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	SyncDir(dir)
	return nil
}

// SyncDir flushes a directory, so that a rename in it survives a power cut. It is best effort:
// some platforms (Windows) cannot open a directory for this, and the rename has happened either
// way.
func SyncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	f.Sync()
	f.Close()
}
