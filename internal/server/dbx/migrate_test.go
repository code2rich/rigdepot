// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package dbx_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
)

var directive = "-- +" + brand.CLI + " "

// testMigrations are three migrations: a table, a row in it (so that running
// it twice shows up as two rows), and a column whose type differs between
// the dialects.
func testMigrations() fstest.MapFS {
	return fstest.MapFS{
		"0001_create.sql": {Data: []byte("-- a table\nCREATE TABLE runs (id TEXT NOT NULL PRIMARY KEY, n INTEGER NOT NULL);\nCREATE INDEX runs_n ON runs (n);\n")},
		"0002_insert.sql": {Data: []byte("INSERT INTO runs (id, n) VALUES ('first', 1);\n")},
		"0003_blob.sql": {Data: []byte(
			directive + "dialect sqlite\n" +
				"CREATE TABLE files (digest TEXT NOT NULL PRIMARY KEY, data BLOB NOT NULL);\n" +
				directive + "dialect postgres\n" +
				"CREATE TABLE files (digest TEXT NOT NULL PRIMARY KEY, data BYTEA NOT NULL);\n" +
				directive + "end\n" +
				"INSERT INTO runs (id, n) VALUES ('third', 3);\n")},
	}
}

func countRuns(t *testing.T, db *dbx.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLoadMigrationsDialectSections(t *testing.T) {
	migs, err := dbx.LoadMigrations(testMigrations())
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) != 3 || migs[2].Version != 3 || migs[2].Name != "0003_blob.sql" {
		t.Fatalf("got %+v", migs)
	}
	lite, pg := migs[2].SQL(dbx.SQLite), migs[2].SQL(dbx.Postgres)
	if !strings.Contains(lite, "BLOB") || strings.Contains(lite, "BYTEA") || !strings.Contains(lite, "'third'") {
		t.Errorf("sqlite statements:\n%s", lite)
	}
	if !strings.Contains(pg, "BYTEA") || strings.Contains(pg, "BLOB") || !strings.Contains(pg, "'third'") {
		t.Errorf("postgres statements:\n%s", pg)
	}
	if strings.Count(lite, "\n") != strings.Count(pg, "\n") {
		t.Error("the two dialects should keep the line numbers of the file")
	}
}

func TestChecksumIgnoresLineEndings(t *testing.T) {
	lf := fstest.MapFS{"0001_a.sql": {Data: []byte("CREATE TABLE a (x TEXT);\nCREATE TABLE b (x TEXT);\n")}}
	crlf := fstest.MapFS{"0001_a.sql": {Data: []byte("CREATE TABLE a (x TEXT);\r\nCREATE TABLE b (x TEXT);\r\n")}}
	a, err1 := dbx.LoadMigrations(lf)
	b, err2 := dbx.LoadMigrations(crlf)
	if err1 != nil || err2 != nil || a[0].Checksum != b[0].Checksum {
		t.Errorf("a checkout with CRLF must not look like a changed migration: %v %v", err1, err2)
	}
}

func TestLoadMigrationsRejects(t *testing.T) {
	tests := map[string]fstest.MapFS{
		"gap":                   {"0001_a.sql": {}, "0003_c.sql": {}},
		"repeat":                {"0001_a.sql": {}, "0001_b.sql": {}},
		"not from one":          {"0002_b.sql": {}},
		"bad name":              {"0001_A.sql": {}},
		"stray file":            {"0001_a.sql": {}, "README.md": {}},
		"unclosed section":      {"0001_a.sql": {Data: []byte(directive + "dialect sqlite\nSELECT 1;\n")}},
		"end without a section": {"0001_a.sql": {Data: []byte(directive + "end\n")}},
		"unknown dialect":       {"0001_a.sql": {Data: []byte(directive + "dialect mysql\n" + directive + "end\n")}},
	}
	for name, fsys := range tests {
		if _, err := dbx.LoadMigrations(fsys); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMigrate(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		ctx := context.Background()
		fsys := testMigrations()

		pending, err := db.Pending(ctx, "test", fsys)
		if err != nil || len(pending) != 3 {
			t.Fatalf("pending on an empty database: %d, %v", len(pending), err)
		}
		applied, err := db.Migrate(ctx, "test", fsys)
		if err != nil || len(applied) != 3 {
			t.Fatalf("applied %d, %v", len(applied), err)
		}
		if v, err := db.SchemaVersion(ctx, "test"); err != nil || v != 3 {
			t.Errorf("schema version %d, %v", v, err)
		}
		if v, err := db.SchemaVersion(ctx, "other"); err != nil || v != 0 {
			t.Errorf("another namespace: %d, %v", v, err)
		}
		// Again: nothing to do.
		applied, err = db.Migrate(ctx, "test", fsys)
		if err != nil || len(applied) != 0 || countRuns(t, db) != 2 {
			t.Errorf("second run applied %d (%v), %d rows", len(applied), err, countRuns(t, db))
		}
		if pending, err := db.Pending(ctx, "test", fsys); err != nil || len(pending) != 0 {
			t.Errorf("pending after migrating: %d, %v", len(pending), err)
		}

		// Upgrade: a fourth file is picked up on its own.
		fsys["0004_more.sql"] = &fstest.MapFile{Data: []byte("INSERT INTO runs (id, n) VALUES ('fourth', 4);\n")}
		applied, err = db.Migrate(ctx, "test", fsys)
		if err != nil || len(applied) != 1 || applied[0].Version != 4 || countRuns(t, db) != 3 {
			t.Errorf("upgrade applied %+v (%v)", applied, err)
		}
	})
}

func TestMigrateFailureRollsBackThatFile(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		ctx := context.Background()
		fsys := fstest.MapFS{
			"0001_ok.sql":     {Data: []byte("CREATE TABLE runs (id TEXT NOT NULL PRIMARY KEY, n INTEGER NOT NULL);\n")},
			"0002_broken.sql": {Data: []byte("INSERT INTO runs (id, n) VALUES ('a', 1);\nINSERT INTO nowhere VALUES (1);\n")},
		}
		applied, err := db.Migrate(ctx, "test", fsys)
		if err == nil || len(applied) != 1 || !strings.Contains(err.Error(), "0002_broken.sql") {
			t.Fatalf("applied %d, err %v", len(applied), err)
		}
		if n := countRuns(t, db); n != 0 {
			t.Errorf("the first statement of the failed file stayed: %d rows", n)
		}
		if v, _ := db.SchemaVersion(ctx, "test"); v != 1 {
			t.Errorf("schema version %d, want 1", v)
		}
	})
}

func TestMigrateRefusesAChangedFile(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		ctx := context.Background()
		fsys := testMigrations()
		if _, err := db.Migrate(ctx, "test", fsys); err != nil {
			t.Fatal(err)
		}
		fsys["0002_insert.sql"] = &fstest.MapFile{Data: []byte("INSERT INTO runs (id, n) VALUES ('changed', 2);\n")}
		if _, err := db.Migrate(ctx, "test", fsys); !errors.Is(err, dbx.ErrChangedMigration) {
			t.Errorf("Migrate: err = %v, want ErrChangedMigration", err)
		}
		if _, err := db.Pending(ctx, "test", fsys); !errors.Is(err, dbx.ErrChangedMigration) {
			t.Errorf("Pending: err = %v, want ErrChangedMigration", err)
		}
	})
}

func TestMigrateRefusesANewerDatabase(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		ctx := context.Background()
		if _, err := db.Migrate(ctx, "test", testMigrations()); err != nil {
			t.Fatal(err)
		}
		older := testMigrations()
		delete(older, "0003_blob.sql")
		if _, err := db.Migrate(ctx, "test", older); !errors.Is(err, dbx.ErrNewerSchema) {
			t.Errorf("Migrate: err = %v, want ErrNewerSchema", err)
		}
		if _, err := db.Pending(ctx, "test", older); !errors.Is(err, dbx.ErrNewerSchema) {
			t.Errorf("Pending: err = %v, want ErrNewerSchema", err)
		}
	})
}

func TestNamespacesAreSeparate(t *testing.T) {
	db := dbxtest.Open(t, dbxtest.SQLiteURL(t))
	ctx := context.Background()
	if _, err := db.Migrate(ctx, "core", fstest.MapFS{"0001_a.sql": {Data: []byte("CREATE TABLE a (x TEXT);")}}); err != nil {
		t.Fatal(err)
	}
	applied, err := db.Migrate(ctx, "insights", fstest.MapFS{"0001_b.sql": {Data: []byte("CREATE TABLE b (x TEXT);")}})
	if err != nil || len(applied) != 1 {
		t.Errorf("a module's first migration: %d, %v", len(applied), err)
	}
}

// TestTwoInstancesMigrateOnce is the acceptance check "two instances
// starting at the same time do not run a migration twice". Both instances
// are lined up in front of every migration's transaction, so they always
// race for it; the loser must find the migration done and skip it.
func TestTwoInstancesMigrateOnce(t *testing.T) {
	for _, target := range dbxtest.Targets(t) {
		t.Run(target.Dialect.String(), func(t *testing.T) {
			const instances = 2
			dbs := make([]*dbx.DB, instances)
			for i := range dbs {
				dbs[i] = dbxtest.Open(t, target.URL)
			}

			// A barrier per migration. It gives up after a while, so that an
			// instance that failed early does not leave the other one waiting.
			var mu sync.Mutex
			type barrier struct {
				arrived int
				all     chan struct{}
			}
			barriers := map[int]*barrier{}
			restore := dbx.SetBeforeMigrationTx(func(version int) {
				mu.Lock()
				b, ok := barriers[version]
				if !ok {
					b = &barrier{all: make(chan struct{})}
					barriers[version] = b
				}
				b.arrived++
				if b.arrived == instances {
					close(b.all)
				}
				mu.Unlock()
				select {
				case <-b.all:
				case <-time.After(5 * time.Second):
				}
			})
			defer restore()

			ctx := context.Background()
			results := make([][]dbx.Migration, instances)
			errs := make([]error, instances)
			var wg sync.WaitGroup
			for i, db := range dbs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					results[i], errs[i] = db.Migrate(ctx, "test", testMigrations())
				}()
			}
			wg.Wait()

			total := 0
			for i := range dbs {
				if errs[i] != nil {
					t.Errorf("instance %d: %v", i, errs[i])
				}
				total += len(results[i])
			}
			if total != 3 {
				t.Errorf("the two instances applied %d migrations between them, want 3", total)
			}
			if n := countRuns(t, dbs[0]); n != 2 {
				t.Errorf("%d rows; a migration ran twice", n)
			}
			var rows int
			if err := dbs[0].QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE namespace = 'test'`).Scan(&rows); err != nil || rows != 3 {
				t.Errorf("schema_migrations has %d rows (%v)", rows, err)
			}
		})
	}
}
