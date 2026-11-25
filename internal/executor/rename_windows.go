// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build windows

package executor

import (
	"os"
	"path/filepath"
	"time"

	"github.com/code2rich/rigdepot/internal/home/winretry"
)

// renameIn renames inside a root. Windows refuses to rename a file or directory while another
// process (the tool, an editor, a virus scanner) has something in it open; that is retried for
// up to winretry.Budget, and never replaced by copying.
func renameIn(root *os.Root, from, to string) error {
	return winretry.Do(func() error {
		return root.Rename(filepath.FromSlash(from), filepath.FromSlash(to))
	}, time.Sleep)
}

// removeAllIn removes a file or directory and everything below it, inside a root, retrying
// while another process holds something in it open. What still cannot be removed is reported
// and left for a later run.
func removeAllIn(root *os.Root, name string) error {
	return winretry.Do(func() error { return root.RemoveAll(filepath.FromSlash(name)) }, time.Sleep)
}
