// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package dbx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	// The two drivers, both pure Go.
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Dialect is the database behind a DB.
type Dialect int

// The two supported databases.
const (
	SQLite Dialect = iota + 1
	Postgres
)

func (d Dialect) String() string {
	switch d {
	case SQLite:
		return "sqlite"
	case Postgres:
		return "postgres"
	}
	return fmt.Sprintf("Dialect(%d)", int(d))
}

// Querier is what domain code runs SQL against: a DB or a Tx. Queries are
// written with ? placeholders in the portable subset of SQL described in
// docs/architecture/06-server.md#怎么用同一套迁移; the dialect rewrites
// them where needed.
type Querier interface {
	Dialect() Dialect
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DB is a database handle with the dialect differences taken care of.
//
// On SQLite it holds two pools: writes go through a pool of one connection
// whose transactions start with BEGIN IMMEDIATE, so that writers queue up
// instead of failing with "database is locked"; reads go through a pool of
// several read-only connections. On PostgreSQL both are the same pool.
type DB struct {
	dialect Dialect
	write   *sql.DB
	read    *sql.DB
}

var (
	_ Querier = (*DB)(nil)
	_ Querier = (*Tx)(nil)
)

// Open connects to the database named by a URL: sqlite://<path to file> or
// postgres://… (postgresql:// too). It checks that the database answers.
func Open(ctx context.Context, rawURL string) (*DB, error) {
	switch {
	case strings.HasPrefix(rawURL, "sqlite://"):
		return openSQLite(ctx, strings.TrimPrefix(rawURL, "sqlite://"))
	case strings.HasPrefix(rawURL, "postgres://"), strings.HasPrefix(rawURL, "postgresql://"):
		return openPostgres(ctx, rawURL)
	}
	return nil, errors.New("dbx: want a sqlite:// or postgres:// URL")
}

// sqliteParams are the settings of docs/architecture/06-server.md: WAL,
// synchronous=NORMAL, a busy timeout of five seconds and foreign keys on.
func sqliteDSN(path string, write bool) string {
	q := url.Values{}
	q.Set("_busy_timeout", "5000")
	q.Set("_journal_mode", "WAL")
	q.Set("_synchronous", "NORMAL")
	q.Set("_foreign_keys", "1")
	if write {
		q.Set("_txlock", "immediate")
	} else {
		q.Set("_query_only", "1")
	}
	return "file:" + filepath.ToSlash(path) + "?" + q.Encode()
}

func openSQLite(ctx context.Context, path string) (*DB, error) {
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		// An in-memory database cannot be shared by the two pools.
		return nil, errors.New("dbx: sqlite:// needs the path of a database file")
	}
	path = filepath.FromSlash(path)
	write, err := sql.Open("sqlite", sqliteDSN(path, true))
	if err != nil {
		return nil, err
	}
	write.SetMaxOpenConns(1)
	// Open the writer first: it switches the file to WAL, which the
	// read-only connections cannot do.
	if err := write.PingContext(ctx); err != nil {
		write.Close()
		return nil, fmt.Errorf("dbx: open %s: %w", path, err)
	}
	read, err := sql.Open("sqlite", sqliteDSN(path, false))
	if err != nil {
		write.Close()
		return nil, err
	}
	read.SetMaxOpenConns(4)
	if err := read.PingContext(ctx); err != nil {
		write.Close()
		read.Close()
		return nil, fmt.Errorf("dbx: open %s: %w", path, err)
	}
	return &DB{dialect: SQLite, write: write, read: read}, nil
}

func openPostgres(ctx context.Context, rawURL string) (*DB, error) {
	pool, err := sql.Open("pgx", rawURL)
	if err != nil {
		// Errors from parsing the URL may quote it, password included.
		return nil, errors.New("dbx: the postgres:// URL is not valid")
	}
	pool.SetMaxOpenConns(16)
	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("dbx: connect to postgres: %w", err)
	}
	return &DB{dialect: Postgres, write: pool, read: pool}, nil
}

// Dialect implements Querier.
func (db *DB) Dialect() Dialect { return db.dialect }

// Close closes both pools.
func (db *DB) Close() error {
	err := db.write.Close()
	if db.read != db.write {
		err = errors.Join(err, db.read.Close())
	}
	return err
}

// Ping checks that the database answers on both pools.
func (db *DB) Ping(ctx context.Context) error {
	if err := db.write.PingContext(ctx); err != nil {
		return err
	}
	return db.read.PingContext(ctx)
}

// ExecContext runs a statement on the write pool, outside a transaction.
func (db *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return db.write.ExecContext(ctx, Rebind(db.dialect, query), args...)
}

// QueryContext runs a query on the read pool. On SQLite the read pool is
// read-only: an INSERT … RETURNING belongs in InTx.
func (db *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return db.read.QueryContext(ctx, Rebind(db.dialect, query), args...)
}

// QueryRowContext runs a query that returns at most one row on the read pool.
func (db *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return db.read.QueryRowContext(ctx, Rebind(db.dialect, query), args...)
}

// Stats returns the statistics of the write pool and the read pool (the same
// pool twice on PostgreSQL), for metrics.
func (db *DB) Stats() (write, read sql.DBStats) {
	return db.write.Stats(), db.read.Stats()
}

// Tx is a write transaction.
type Tx struct {
	dialect     Dialect
	tx          *sql.Tx
	afterCommit []func()
}

// InTx runs fn in a write transaction and commits it when fn returns nil. It
// rolls back when fn returns an error or panics; the panic continues. On
// SQLite the transaction holds the database's write lock from the start.
// Functions registered with Tx.AfterCommit run once the commit succeeded,
// before InTx returns.
func (db *DB) InTx(ctx context.Context, fn func(*Tx) error) (err error) {
	sqlTx, err := db.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("dbx: begin: %w", err)
	}
	tx := &Tx{dialect: db.dialect, tx: sqlTx}
	defer func() {
		if p := recover(); p != nil {
			sqlTx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		if rbErr := sqlTx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("dbx: rollback: %w", rbErr))
		}
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("dbx: commit: %w", err)
	}
	for _, f := range tx.afterCommit {
		f()
	}
	return nil
}

// AfterCommit registers f to run after the transaction has committed, in the
// order of registration. When the transaction rolls back, f never runs. Domain
// events use it so that nobody hears of a change that did not happen. f runs
// on the goroutine that called InTx and must not block.
func (tx *Tx) AfterCommit(f func()) { tx.afterCommit = append(tx.afterCommit, f) }

// Dialect implements Querier.
func (tx *Tx) Dialect() Dialect { return tx.dialect }

// ExecContext implements Querier.
func (tx *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.tx.ExecContext(ctx, Rebind(tx.dialect, query), args...)
}

// QueryContext implements Querier.
func (tx *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.tx.QueryContext(ctx, Rebind(tx.dialect, query), args...)
}

// QueryRowContext implements Querier.
func (tx *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.tx.QueryRowContext(ctx, Rebind(tx.dialect, query), args...)
}

// execScript runs several statements at once, without arguments and
// without placeholder rewriting. Migrations use it.
func (tx *Tx) execScript(ctx context.Context, script string) error {
	_, err := tx.tx.ExecContext(ctx, script)
	return err
}

// Lock takes a lock named by key that is held until the transaction ends,
// so that only one transaction (in any process) works on that key at a
// time. Use it when the next value of a counter is read and written in the
// same transaction, such as the sequence number of the audit log.
//
// On SQLite every write transaction already holds the database's write lock,
// so this does nothing. On PostgreSQL it is a transaction-level advisory lock
// on the key within the current schema.
func (tx *Tx) Lock(ctx context.Context, key string) error {
	if tx.dialect != Postgres {
		return nil
	}
	_, err := tx.tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_schema() || ':' || $1, 0))`, key)
	if err != nil {
		return fmt.Errorf("dbx: lock %q: %w", key, err)
	}
	return nil
}
