// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package winretry retries file operations that Windows refuses for a moment because another
// process has the file open: a tool that is running, an editor, a virus scanner.
//
// The logic is plain Go and is tested on every platform; only the error codes are Windows'.
package winretry

import (
	"errors"
	"syscall"
	"time"
)

// Budget is the longest Do keeps retrying.
const Budget = 2 * time.Second

// The Windows error codes that mean "somebody else has it open right now".
const (
	errAccessDenied     = syscall.Errno(5)  // ERROR_ACCESS_DENIED: also what a pending delete looks like
	errSharingViolation = syscall.Errno(32) // ERROR_SHARING_VIOLATION
	errLockViolation    = syscall.Errno(33) // ERROR_LOCK_VIOLATION
)

// Transient reports whether err is one of the Windows errors that go away once another process
// lets go of the file.
func Transient(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == errAccessDenied || errno == errSharingViolation || errno == errLockViolation
}

// Do runs op, and runs it again after a growing pause while it fails with a transient error,
// until it succeeds, fails otherwise, or Budget is spent. It returns op's last error.
func Do(op func() error, sleep func(time.Duration)) error {
	wait := 10 * time.Millisecond
	var spent time.Duration
	for {
		err := op()
		if err == nil || !Transient(err) || spent >= Budget {
			return err
		}
		if spent+wait > Budget {
			wait = Budget - spent
		}
		sleep(wait)
		spent += wait
		wait *= 2
	}
}
