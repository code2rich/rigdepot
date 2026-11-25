// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package manifest

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/code2rich/rigdepot/internal/home"
)

// ErrLocked is satisfied, with errors.Is, by the error Store.Lock returns when another program
// holds the lock for longer than it was willing to wait. Such an error satisfies ErrUnknown as
// well: while somebody else is changing things, the state is not known.
var ErrLocked = errors.New("another program is changing the local state")

// DefaultWait is how long a program waits for the lock before it gives up.
const DefaultWait = 10 * time.Second

// pollInterval is how often a waiting program tries the lock again. The operating systems'
// blocking lock calls cannot be cancelled, so waiting is a loop of attempts that fail at once.
const pollInterval = 25 * time.Millisecond

// lockFile is the open lock file. The lock belongs to the open file, so it ends when the file
// is closed or the process ends, whichever comes first.
type lockFile struct {
	f *os.File
}

// acquire opens the lock file and takes the lock, trying until wait has passed or ctx ends.
// The lock file itself is never removed: removing it while another process waits on it would
// let two processes hold "the" lock on two different files.
func acquire(ctx context.Context, path string, wait time.Duration) (*lockFile, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, home.FilePerm)
	if err != nil {
		return nil, unknown(path, Unreadable, err)
	}
	deadline := time.Now().Add(wait)
	for {
		ok, err := tryLock(f)
		if err != nil {
			f.Close()
			return nil, unknown(path, Unreadable, err)
		}
		if ok {
			return &lockFile{f: f}, nil
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, unknown(path, LockHeld, ErrLocked)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(min(pollInterval, time.Until(deadline))):
		}
	}
}

func (l *lockFile) release() error {
	err := unlock(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	return err
}
