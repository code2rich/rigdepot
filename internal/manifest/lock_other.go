// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build !(darwin || linux || freebsd || openbsd || netbsd || dragonfly || windows)

package manifest

import (
	"errors"
	"os"
)

// On platforms without a lock this program knows how to take, nothing may be changed: two
// programs changing the manifest at once is worse than not changing it.
func tryLock(*os.File) (bool, error) {
	return false, errors.New("cross-process locking is not implemented on this platform")
}

func unlock(*os.File) error { return nil }
