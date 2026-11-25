// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build !windows

package home

import "os"

// Rename renames oldpath to newpath, replacing newpath. On macOS and Linux rename(2) is atomic
// and succeeds even when another process has newpath open.
func Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}
