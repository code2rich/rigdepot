// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build windows

package server

import (
	"io/fs"

	"golang.org/x/sys/windows"
)

// freeSpace returns the bytes available to this user on the volume of path.
func freeSpace(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, err
	}
	return free, nil
}

// fileOwner is not checked on Windows, which has access lists instead of an owner and mode.
func fileOwner(fs.FileInfo) (int, bool) { return 0, false }
