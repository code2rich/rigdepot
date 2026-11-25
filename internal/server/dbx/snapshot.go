// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package dbx

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Snapshots of a SQLite database (docs/architecture/08-deployment.md#备份与恢复): a consistent
// copy made with VACUUM INTO while the server keeps running, and a way to open such a copy
// without changing it.

// ErrSnapshotUnsupported is returned by Snapshot on PostgreSQL, which is backed up with its own
// tools (pg_dump, or the snapshots of a managed service).
var ErrSnapshotUnsupported = errors.New("dbx: snapshots are for SQLite; back up PostgreSQL with pg_dump or your provider's snapshots")

// Path returns the SQLite database file, or "" on PostgreSQL.
func (db *DB) Path() string { return db.path }

// Snapshot writes a consistent copy of the SQLite database to dest, which must not exist yet.
// It runs VACUUM INTO on a connection of its own, in a read transaction: in WAL mode writers
// carry on meanwhile, and the copy is the database as of the moment the transaction started.
// The copy is a single file (no -wal or -shm), readable by the server's user alone (0600). It
// is written next to dest under a temporary name and renamed when complete, so that a
// half-written copy never carries a backup's name.
func (db *DB) Snapshot(ctx context.Context, dest string) error {
	if db.dialect != SQLite {
		return ErrSnapshotUnsupported
	}
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("dbx: snapshot: %s already exists", dest)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("dbx: snapshot: %w", err)
	}
	var suffix [6]byte
	rand.Read(suffix[:])
	tmp := filepath.Join(filepath.Dir(dest), "."+filepath.Base(dest)+".partial-"+hex.EncodeToString(suffix[:]))
	// VACUUM INTO writes into an empty file that exists; creating it here sets its mode.
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("dbx: snapshot: %w", err)
	}
	f.Close()
	done := false
	defer func() {
		if !done {
			os.Remove(tmp)
		}
	}()

	// Not the read pool: its connections are query_only, which VACUUM INTO refuses. Not the
	// write pool: that would hold up every write for as long as the copy takes.
	conn, err := sql.Open("sqlite", sqliteSnapshotDSN(db.path))
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return fmt.Errorf("dbx: snapshot: %w", err)
	}
	// VACUUM INTO does not sync the copy.
	if err := syncFile(tmp); err != nil {
		return fmt.Errorf("dbx: snapshot: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("dbx: snapshot: %w", err)
	}
	done = true
	return nil
}

func sqliteSnapshotDSN(path string) string {
	return "file:" + filepath.ToSlash(path) + "?_busy_timeout=5000"
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	return errors.Join(err, f.Close())
}

// ErrNotADatabase is returned by OpenSnapshot for a file that is not a SQLite database.
var ErrNotADatabase = errors.New("not a SQLite database")

// OpenSnapshot opens a SQLite snapshot read-only and without changing it in any way: no
// journal, no -wal or -shm next to it, no locks (the file is taken as immutable). Every write
// through the returned DB fails. It is for checking a backup before it is restored.
func OpenSnapshot(ctx context.Context, path string) (*DB, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("dbx: open snapshot: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("dbx: open snapshot: %s is not a file", path)
	}
	pool, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&immutable=1&_query_only=1")
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(1)
	// Opening is lazy; reading the schema is what finds a file that is not a database.
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
		pool.Close()
		return nil, fmt.Errorf("dbx: open snapshot %s: %w: %v", path, ErrNotADatabase, err)
	}
	return &DB{dialect: SQLite, write: pool, read: pool, path: path}, nil
}

// IntegrityCheck runs SQLite's integrity check and returns the problems it finds (none for a
// sound database). On PostgreSQL it returns nothing: the server has no such check there.
func (db *DB) IntegrityCheck(ctx context.Context) ([]string, error) {
	if db.dialect != SQLite {
		return nil, nil
	}
	rows, err := db.read.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if s != "ok" {
			problems = append(problems, s)
		}
	}
	return problems, rows.Err()
}
