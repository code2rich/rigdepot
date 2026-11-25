// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build windows

package home

import (
	"os"
	"time"

	"github.com/code2rich/rigdepot/internal/home/winretry"
)

// Rename renames oldpath to newpath, replacing newpath. On Windows os.Rename is
// MoveFileEx(MOVEFILE_REPLACE_EXISTING), which fails while another process holds newpath open
// without sharing delete access, or while a virus scanner looks at oldpath. Those failures are
// retried with backoff for up to winretry.Budget. It never falls back to overwriting newpath in
// place: a failure halfway through that would leave half a file.
func Rename(oldpath, newpath string) error {
	return winretry.Do(func() error { return os.Rename(oldpath, newpath) }, time.Sleep)
}
