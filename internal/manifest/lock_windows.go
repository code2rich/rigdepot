// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build windows

package manifest

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLock takes an exclusive LockFileEx lock on the first byte of f without waiting. The kernel
// releases it when the handle is closed or the process ends.
func tryLock(f *os.File) (bool, error) {
	var ol windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return false, nil
	}
	return false, err
}

func unlock(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}
