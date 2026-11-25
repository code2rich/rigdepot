// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build unix

package bundle

import (
	"io/fs"
	"syscall"
)

// linkCount returns how many names the file has on disk, or 1 when that is not known.
func linkCount(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && !fi.IsDir() {
		return uint64(st.Nlink)
	}
	return 1
}
