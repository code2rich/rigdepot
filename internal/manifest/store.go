// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/code2rich/rigdepot/internal/home"
)

// Store is the manifest, its intent log and its lock in one local data directory.
type Store struct {
	dir home.Dir
}

// Open returns the store in dir. It does not touch the disk.
func Open(dir home.Dir) *Store {
	return &Store{dir: dir}
}

// Dir returns the local data directory of the store.
func (s *Store) Dir() home.Dir { return s.dir }

// Read reads the manifest without the lock, for commands that only look. See ReadFile for what
// it returns when the manifest is missing or cannot be trusted.
func (s *Store) Read() (*Manifest, error) {
	return ReadFile(s.dir.Join(home.ManifestFile))
}

// Lock takes the cross-process lock, waiting up to wait for another program to let go. Every
// change to the manifest, and every write the manifest is to record, happens while it is held.
//
// When the lock cannot be had in time, the error satisfies both ErrLocked and ErrUnknown.
func (s *Store) Lock(ctx context.Context, wait time.Duration) (*Locked, error) {
	if err := s.dir.Ensure(); err != nil {
		return nil, unknown(s.dir.Path(), Unreadable, err)
	}
	lf, err := acquire(ctx, s.dir.Join(home.LockFile), wait)
	if err != nil {
		return nil, err
	}
	return &Locked{s: s, lock: lf}, nil
}

// Locked is the store while this process holds the lock. Only it can change the manifest and
// the intent log.
type Locked struct {
	s    *Store
	lock *lockFile
}

var errReleased = errors.New("manifest: the lock was already released")

// Release lets go of the lock. Calling it again does nothing.
func (l *Locked) Release() error {
	if l.lock == nil {
		return nil
	}
	err := l.lock.release()
	l.lock = nil
	return err
}

func (l *Locked) held() error {
	if l.lock == nil {
		return errReleased
	}
	return nil
}

// Read reads the manifest. With the lock held, what it returns stays true until this process
// changes it.
func (l *Locked) Read() (*Manifest, error) {
	if err := l.held(); err != nil {
		return nil, err
	}
	return l.s.Read()
}

// Write replaces the manifest with m, atomically. The manifest as it was is kept as
// home.ManifestPrevious. m.Schema is set to Version.
//
// A manifest that does not match the schema is not written: that would be a defect in this
// program, and writing it would make every later run see an unknown state.
func (l *Locked) Write(m *Manifest) error {
	if err := l.held(); err != nil {
		return err
	}
	m.Schema = Version
	if err := check(m); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := l.s.dir.Join(home.ManifestFile)
	old, err := os.ReadFile(path)
	switch {
	case err == nil:
		if bytes.Equal(old, data) {
			return nil
		}
		if err := home.WriteFileAtomic(l.s.dir.Join(home.ManifestPrevious), old, home.FilePerm); err != nil {
			return err
		}
	case !errors.Is(err, fs.ErrNotExist):
		return unknown(path, Unreadable, err)
	}
	return home.WriteFileAtomic(path, data, home.FilePerm)
}

// update reads the manifest, lets change modify it and writes it back.
func (l *Locked) update(change func(*Manifest)) error {
	m, err := l.Read()
	if err != nil {
		return err
	}
	change(m)
	return l.Write(m)
}
