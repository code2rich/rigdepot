// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package dbx_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
)

// TestSnapshotWhileWriting: a snapshot taken while another goroutine keeps writing is a
// complete database of its own, holds a prefix of the writes, and is readable by the owner
// alone.
func TestSnapshotWhileWriting(t *testing.T) {
	db := dbxtest.Open(t, dbxtest.SQLiteURL(t))
	ctx := withItems(t, db)
	insert := func(i int) error {
		return db.InTx(ctx, func(tx *dbx.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO items (id, name, created_at) VALUES (?, ?, ?)`, dbx.NewID(), fmt.Sprintf("item-%04d", i), i)
			return err
		})
	}
	for i := range 200 {
		if err := insert(i); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 200; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := insert(i); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	dest := filepath.Join(t.TempDir(), "snap.db")
	err := db.Snapshot(ctx, dest)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}

	snap, err := dbx.OpenSnapshot(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	var n, maxCreated int
	if err := snap.QueryRowContext(ctx, `SELECT count(*), max(created_at) FROM items`).Scan(&n, &maxCreated); err != nil {
		t.Fatal(err)
	}
	if n < 200 || maxCreated != n-1 {
		t.Errorf("the snapshot has %d items, the last created at %d: not a prefix of the writes", n, maxCreated)
	}
	if problems, err := snap.IntegrityCheck(ctx); err != nil || len(problems) > 0 {
		t.Errorf("integrity: %v %v", problems, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(dest); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("snapshot mode %v %v, want 0600", info.Mode(), err)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 1 {
		t.Errorf("files next to the snapshot: %v", entries)
	}
}

func TestSnapshotRefusesAnExistingFile(t *testing.T) {
	db := dbxtest.Open(t, dbxtest.SQLiteURL(t))
	ctx := withItems(t, db)
	dest := filepath.Join(t.TempDir(), "snap.db")
	if err := os.WriteFile(dest, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.Snapshot(ctx, dest); err == nil {
		t.Fatal("overwrote an existing file")
	}
	if b, _ := os.ReadFile(dest); string(b) != "keep me" {
		t.Errorf("the existing file changed: %q", b)
	}
	// A directory that does not exist: an error, and nothing left behind.
	if err := db.Snapshot(ctx, filepath.Join(t.TempDir(), "missing", "snap.db")); err == nil {
		t.Error("snapshot into a missing directory succeeded")
	}
}

func TestSnapshotIsForSQLite(t *testing.T) {
	db := dbxtest.Open(t, dbxtest.PostgresURL(t))
	if err := db.Snapshot(context.Background(), filepath.Join(t.TempDir(), "x.db")); !errors.Is(err, dbx.ErrSnapshotUnsupported) {
		t.Errorf("err = %v", err)
	}
}

// TestOpenSnapshotChangesNothing: checking a backup leaves its bytes alone and creates nothing
// next to it; writes through it fail.
func TestOpenSnapshotChangesNothing(t *testing.T) {
	db := dbxtest.Open(t, dbxtest.SQLiteURL(t))
	ctx := withItems(t, db)
	dir := t.TempDir()
	dest := filepath.Join(dir, "snap.db")
	if err := db.Snapshot(ctx, dest); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(dest)
	snap, err := dbx.OpenSnapshot(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snap.ExecContext(ctx, `INSERT INTO items (id, name, created_at) VALUES ('a', 'b', 1)`); err == nil {
		t.Error("a write through a snapshot succeeded")
	}
	snap.Close()
	after, _ := os.ReadFile(dest)
	if !bytes.Equal(before, after) {
		t.Error("opening the snapshot changed it")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("files next to the snapshot: %v", entries)
	}
}

func TestOpenSnapshotRefusesWhatIsNotADatabase(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.db")
	if err := os.WriteFile(junk, bytes.Repeat([]byte("not a database "), 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.OpenSnapshot(context.Background(), junk); !errors.Is(err, dbx.ErrNotADatabase) {
		t.Errorf("junk: %v", err)
	}
	if _, err := dbx.OpenSnapshot(context.Background(), filepath.Join(dir, "missing.db")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if _, err := dbx.OpenSnapshot(context.Background(), dir); err == nil {
		t.Error("a directory opened as a snapshot")
	}
}
