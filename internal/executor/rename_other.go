// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build !windows

package executor

import (
	"os"
	"path/filepath"
)

// renameIn renames inside a root. On macOS and Linux rename(2) is atomic and does not care
// whether another process has the files open.
func renameIn(root *os.Root, from, to string) error {
	return root.Rename(filepath.FromSlash(from), filepath.FromSlash(to))
}

// removeAllIn removes a file or directory and everything below it, inside a root.
func removeAllIn(root *os.Root, name string) error {
	return root.RemoveAll(filepath.FromSlash(name))
}
