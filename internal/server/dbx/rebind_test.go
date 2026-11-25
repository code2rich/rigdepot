// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package dbx_test

import (
	"testing"

	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func TestRebind(t *testing.T) {
	tests := []struct{ in, want string }{
		{`SELECT 1`, `SELECT 1`},
		{`SELECT * FROM t WHERE a = ? AND b = ?`, `SELECT * FROM t WHERE a = $1 AND b = $2`},
		{`INSERT INTO t (a, b, c) VALUES (?, ?, ?) ON CONFLICT (a) DO UPDATE SET b = excluded.b`,
			`INSERT INTO t (a, b, c) VALUES ($1, $2, $3) ON CONFLICT (a) DO UPDATE SET b = excluded.b`},
		{`SELECT '?' , ? FROM t`, `SELECT '?' , $1 FROM t`},
		{`SELECT 'it''s ?', ?`, `SELECT 'it''s ?', $1`},
		{`SELECT "odd?name" FROM t WHERE x = ?`, `SELECT "odd?name" FROM t WHERE x = $1`},
		{"SELECT ? -- why?\nFROM t WHERE y = ?", "SELECT $1 -- why?\nFROM t WHERE y = $2"},
		{`SELECT ? /* a ? b */ , ?`, `SELECT $1 /* a ? b */ , $2`},
		{`SELECT $$ ? $$, ?`, `SELECT $$ ? $$, $1`},
		{`SELECT $tag$ ? $tag$, ?`, `SELECT $tag$ ? $tag$, $1`},
		{`SELECT ?, $1`, `SELECT $1, $1`}, // an existing $1 is not a dollar quote
		{`SELECT 'unterminated ?`, `SELECT 'unterminated ?`},
		{`LOWER(name) LIKE LOWER(?)`, `LOWER(name) LIKE LOWER($1)`},
	}
	for _, tt := range tests {
		if got := dbx.Rebind(dbx.Postgres, tt.in); got != tt.want {
			t.Errorf("Rebind(%q)\n got %q\nwant %q", tt.in, got, tt.want)
		}
		if got := dbx.Rebind(dbx.SQLite, tt.in); got != tt.in {
			t.Errorf("SQLite must keep the query as written: %q became %q", tt.in, got)
		}
	}
}

func TestRebindTenOrMore(t *testing.T) {
	in := `VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	want := `VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	if got := dbx.Rebind(dbx.Postgres, in); got != want {
		t.Errorf("got %q", got)
	}
}
