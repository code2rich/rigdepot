// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package dbxtest gives storage tests a fresh, empty database on each
// dialect: always SQLite (a file in a temporary directory) and PostgreSQL
// when one is configured.
//
// PostgreSQL tests run when RGD_TEST_POSTGRES_DSN holds a postgres:// URL of
// a database on this machine (loopback or a Unix socket; tests do not use
// the network). Each test gets its own schema in that database, dropped when
// the test ends. Without the variable those tests are skipped, and the skip
// says how to run them. Nothing here installs or starts a database.
package dbxtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// PostgresEnv names the variable with the PostgreSQL test database.
var PostgresEnv = brand.EnvPrefix + "TEST_POSTGRES_DSN"

// Target is one database to run a storage test against.
type Target struct {
	Dialect dbx.Dialect
	URL     string // a fresh, empty database; open it as often as needed
}

// Targets returns a fresh empty database for every dialect available: SQLite
// always, PostgreSQL when configured. The databases are removed when the test
// ends.
func Targets(t testing.TB) []Target {
	t.Helper()
	out := []Target{{dbx.SQLite, SQLiteURL(t)}}
	if _, ok := os.LookupEnv(PostgresEnv); ok {
		out = append(out, Target{dbx.Postgres, PostgresURL(t)})
	}
	return out
}

// Each runs fn as a subtest on every available dialect, each time with a
// fresh, empty, open database. When PostgreSQL is not configured, its
// subtest is reported as skipped.
func Each(t *testing.T, fn func(t *testing.T, db *dbx.DB)) {
	t.Helper()
	t.Run(dbx.SQLite.String(), func(t *testing.T) { fn(t, Open(t, SQLiteURL(t))) })
	t.Run(dbx.Postgres.String(), func(t *testing.T) { fn(t, Open(t, PostgresURL(t))) })
}

// Open opens a database for the length of the test.
func Open(t testing.TB, rawURL string) *dbx.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := dbx.Open(ctx, rawURL)
	if err != nil {
		t.Fatalf("dbxtest: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// SQLiteURL returns the URL of a new SQLite database file in a temporary
// directory of the test.
func SQLiteURL(t testing.TB) string {
	t.Helper()
	return "sqlite://" + filepath.ToSlash(filepath.Join(t.TempDir(), "test.db"))
}

// PostgresURL returns the URL of a new, empty schema in the PostgreSQL test
// database, or skips the test when there is none. The schema is dropped when
// the test ends.
func PostgresURL(t testing.TB) string {
	t.Helper()
	dsn, ok := os.LookupEnv(PostgresEnv)
	if !ok || dsn == "" {
		t.Skipf("no PostgreSQL to test against; set %s=postgres://<user>@127.0.0.1:5432/<database> to run this test", PostgresEnv)
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatalf("dbxtest: %s must be a postgres:// URL", PostgresEnv)
	}
	if !local(u) {
		t.Fatalf("dbxtest: %s must point at this machine (loopback or a Unix socket); tests do not use the network", PostgresEnv)
	}
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	schema := brand.CLI + "_test_" + hex.EncodeToString(b[:])

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := dbx.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("dbxtest: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatalf("dbxtest: create schema: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("dbxtest: drop schema %s: %v", schema, err)
		}
		admin.Close()
	})
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func local(u *url.URL) bool {
	if h := u.Query().Get("host"); strings.HasPrefix(h, "/") {
		return true
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}
