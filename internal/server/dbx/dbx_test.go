// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package dbx_test

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
)

const itemsTable = `CREATE TABLE items (
    id         TEXT    NOT NULL PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    done       BOOLEAN NOT NULL DEFAULT FALSE,
    hits       INTEGER NOT NULL DEFAULT 0,
    created_at BIGINT  NOT NULL
)`

func withItems(t *testing.T, db *dbx.DB) context.Context {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, itemsTable); err != nil {
		t.Fatal(err)
	}
	return ctx
}

// TestPortableSubset runs the idioms of the portable subset on both
// databases: placeholders, booleans, times, upsert with excluded,
// RETURNING, case-insensitive search.
func TestPortableSubset(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		ctx := withItems(t, db)
		now := time.Date(2026, 11, 25, 8, 30, 0, 123e6, time.UTC)
		var id string
		err := db.InTx(ctx, func(tx *dbx.Tx) error {
			return tx.QueryRowContext(ctx,
				`INSERT INTO items (id, name, done, created_at) VALUES (?, ?, ?, ?) RETURNING id`,
				dbx.NewID(), "Alpha", true, dbx.Millis(now)).Scan(&id)
		})
		if err != nil {
			t.Fatal(err)
		}
		// Upsert: the second insert of the same name bumps the counter.
		for range 2 {
			if _, err := db.ExecContext(ctx,
				`INSERT INTO items (id, name, done, hits, created_at) VALUES (?, ?, ?, ?, ?)
				 ON CONFLICT (name) DO UPDATE SET hits = items.hits + excluded.hits, done = excluded.done`,
				dbx.NewID(), "Alpha", false, 1, dbx.Millis(now)); err != nil {
				t.Fatal(err)
			}
		}
		var (
			done    bool
			hits    int
			created int64
		)
		if err := db.QueryRowContext(ctx, `SELECT done, hits, created_at FROM items WHERE LOWER(name) LIKE LOWER(?)`, "alp%").
			Scan(&done, &hits, &created); err != nil {
			t.Fatal(err)
		}
		if done || hits != 2 || !dbx.Time(created).Equal(now) {
			t.Errorf("done %v hits %d created %v; want false, 2, %v", done, hits, dbx.Time(created), now)
		}
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE done = FALSE AND id = ?`, id).Scan(&n); err != nil || n != 1 {
			t.Errorf("boolean literal: n=%d err=%v", n, err)
		}
	})
}

func TestUniqueViolation(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		ctx := withItems(t, db)
		insert := `INSERT INTO items (id, name, created_at) VALUES (?, ?, 0)`
		if _, err := db.ExecContext(ctx, insert, "a", "x"); err != nil {
			t.Fatal(err)
		}
		_, err := db.ExecContext(ctx, insert, "b", "x")
		if !dbx.IsUniqueViolation(err) {
			t.Errorf("duplicate name: IsUniqueViolation(%v) = false", err)
		}
		_, err = db.ExecContext(ctx, insert, "a", "y")
		if !dbx.IsUniqueViolation(err) {
			t.Errorf("duplicate key: IsUniqueViolation(%v) = false", err)
		}
		if dbx.IsUniqueViolation(errors.New("x")) || dbx.IsUniqueViolation(nil) {
			t.Error("IsUniqueViolation is true for an unrelated error")
		}
	})
}

func TestInTx(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		ctx := withItems(t, db)
		count := func() int {
			var n int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		insert := func(tx *dbx.Tx, name string) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO items (id, name, created_at) VALUES (?, ?, 0)`, dbx.NewID(), name)
			return err
		}

		boom := errors.New("boom")
		err := db.InTx(ctx, func(tx *dbx.Tx) error {
			if err := insert(tx, "a"); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) || count() != 0 {
			t.Errorf("error: err=%v count=%d; want boom and a rollback", err, count())
		}

		func() {
			defer func() {
				if recover() == nil {
					t.Error("the panic did not continue")
				}
			}()
			_ = db.InTx(ctx, func(tx *dbx.Tx) error {
				if err := insert(tx, "b"); err != nil {
					return err
				}
				panic("boom")
			})
		}()
		if count() != 0 {
			t.Errorf("panic: count=%d, want a rollback", count())
		}

		err = db.InTx(ctx, func(tx *dbx.Tx) error {
			if err := tx.Lock(ctx, "items"); err != nil {
				return err
			}
			return insert(tx, "c")
		})
		if err != nil || count() != 1 {
			t.Errorf("commit: err=%v count=%d", err, count())
		}
	})
}

func TestSQLiteReadPoolIsReadOnly(t *testing.T) {
	db := dbxtest.Open(t, dbxtest.SQLiteURL(t))
	ctx := withItems(t, db)
	var id string
	err := db.QueryRowContext(ctx, `INSERT INTO items (id, name, created_at) VALUES ('x', 'x', 0) RETURNING id`).Scan(&id)
	if err == nil {
		t.Error("a write through the read pool succeeded; it must go through InTx")
	}
}

func TestOpenRejects(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{"mysql://x", "sqlite://", "sqlite://:memory:", "postgres://%zz"} {
		if db, err := dbx.Open(ctx, u); err == nil {
			db.Close()
			t.Errorf("Open(%q) succeeded", u)
		}
	}
	if _, err := dbx.Open(ctx, "postgres://app:sEcReT@%zz/x"); err == nil || strings.Contains(err.Error(), "sEcReT") {
		t.Errorf("error repeats the password or is missing: %v", err)
	}
}

var uuidV7 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewID(t *testing.T) {
	ids := make([]string, 2000)
	seen := map[string]bool{}
	for i := range ids {
		ids[i] = dbx.NewID()
		if !uuidV7.MatchString(ids[i]) {
			t.Fatalf("%q is not a UUIDv7", ids[i])
		}
		if seen[ids[i]] {
			t.Fatalf("duplicate %q", ids[i])
		}
		seen[ids[i]] = true
	}
	if !sort.StringsAreSorted(ids) {
		t.Error("IDs made one after another do not sort in order")
	}
}

func TestStats(t *testing.T) {
	db := dbxtest.Open(t, dbxtest.SQLiteURL(t))
	w, r := db.Stats()
	if w.MaxOpenConnections != 1 || r.MaxOpenConnections < 2 {
		t.Errorf("write pool %d, read pool %d connections; want one writer and several readers", w.MaxOpenConnections, r.MaxOpenConnections)
	}
	if err := db.Ping(context.Background()); err != nil {
		t.Error(err)
	}
}
