// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package winretry

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func TestDoRetriesASharingViolationUntilItClears(t *testing.T) {
	calls := 0
	var slept time.Duration
	err := Do(func() error {
		calls++
		if calls < 4 {
			return &os.LinkError{Op: "rename", Old: "a", New: "b", Err: errSharingViolation}
		}
		return nil
	}, func(d time.Duration) { slept += d })
	if err != nil {
		t.Fatalf("Do = %v, want success once the other process lets go", err)
	}
	if calls != 4 {
		t.Errorf("op ran %d times, want 4", calls)
	}
	if slept <= 0 || slept > Budget {
		t.Errorf("slept %v in total, want something within %v", slept, Budget)
	}
}

func TestDoGivesUpAfterTheBudget(t *testing.T) {
	var slept time.Duration
	calls := 0
	err := Do(func() error {
		calls++
		return fmt.Errorf("rename: %w", errLockViolation)
	}, func(d time.Duration) { slept += d })
	if !errors.Is(err, errLockViolation) {
		t.Fatalf("Do = %v, want the last error", err)
	}
	if slept != Budget {
		t.Errorf("slept %v in total, want exactly the budget %v", slept, Budget)
	}
	if calls < 3 {
		t.Errorf("op ran %d times; it should have been retried", calls)
	}
}

func TestDoDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	err := Do(func() error {
		calls++
		return syscall.Errno(2) // ERROR_FILE_NOT_FOUND
	}, func(time.Duration) { t.Fatal("slept for an error that will not go away") })
	if err == nil || calls != 1 {
		t.Fatalf("Do = %v after %d calls, want the error after one call", err, calls)
	}
	if Do(func() error { return nil }, nil) != nil {
		t.Fatal("Do failed for an operation that succeeded")
	}
}
