// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package migrations_test

import (
	"context"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

var update = flag.Bool("update", false, "rewrite testdata/schema.golden from SQLite")

const golden = "testdata/schema.golden"

func migrated(t *testing.T, db *dbx.DB) dbx.Schema {
	t.Helper()
	ctx := context.Background()
	if _, err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	s, err := db.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSchemaMatchesGolden keeps the schema both databases end up with in one
// reviewed file. SQLite always checks against it; PostgreSQL does too when
// it is available, which is how the two are kept from drifting apart even on
// a machine without PostgreSQL. After adding a migration, regenerate it with
// go test ./internal/server/migrations -run TestSchemaMatchesGolden -update
// and review the diff.
func TestSchemaMatchesGolden(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		got := migrated(t, db).String()
		if *update && db.Dialect() == dbx.SQLite {
			if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run with -update to create it)", err)
		}
		if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
			t.Errorf("the %s schema differs from %s:\n%s", db.Dialect(), golden, lineDiff(string(want), got))
		}
	})
}

// TestSameSchemaOnBothDatabases is the acceptance check "the two databases
// have the same tables, columns and nullability after migrating". It needs
// PostgreSQL; without it, TestSchemaMatchesGolden still pins SQLite.
func TestSameSchemaOnBothDatabases(t *testing.T) {
	pgURL := dbxtest.PostgresURL(t)
	lite := migrated(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	pg := migrated(t, dbxtest.Open(t, pgURL))
	if diff := dbx.Diff(lite, pg); len(diff) > 0 {
		t.Errorf("SQLite (first) and PostgreSQL (second) differ:\n  %s", strings.Join(diff, "\n  "))
	}
}

// TestPrimaryKeysAreNotNull checks what PostgreSQL would enforce on its own:
// SQLite lets a TEXT primary key be NULL unless the column says NOT NULL.
func TestPrimaryKeysAreNotNull(t *testing.T) {
	s := migrated(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	for table, cols := range s {
		for _, c := range cols {
			if c.PrimaryKey && !c.NotNull {
				t.Errorf("%s.%s is a primary key without NOT NULL", table, c.Name)
			}
		}
	}
}

// TestEveryTableOfTheDataModel checks the first migration against the data
// model of docs/architecture/06-server.md#数据模型.
func TestEveryTableOfTheDataModel(t *testing.T) {
	s := migrated(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	for _, table := range []string{
		"users", "identities", "local_credentials", "invites", "invite_products", "sessions", "access_tokens",
		"devices", "device_codes", "products", "memberships", "join_requests",
		"sources", "resources", "resource_stages", "resource_maintainers", "resource_drafts",
		"resource_versions", "resource_refs", "blobs", "stages", "check_waivers",
		"user_resource_prefs", "installs", "secret_defs", "resource_secrets", "secret_values",
		"feedback", "feedback_events", "audit_log", "notifications", "mail_outbox",
		"settings", "jobs", "schema_migrations",
	} {
		if _, ok := s[table]; !ok {
			t.Errorf("table %s is missing", table)
		}
	}
}

func insertAudit(ctx context.Context, q dbx.Querier, seq int) error {
	_, err := q.ExecContext(ctx, `INSERT INTO audit_log
		(seq, at, category, actor_id, origin, action, object_kind, object_id, object_label, result, prev_hash, hash)
		VALUES (?, ?, 'platform', NULL, 'system', 'settings.update', 'setting', 'k', 'k', 'ok', ?, ?)`,
		seq, 1, strings.Repeat("0", 64), strings.Repeat("1", 64))
	return err
}

// TestAuditLogRefusesUpdateAndDelete: the database itself keeps the audit
// log append-only (06-server.md#审计), whatever the program does.
func TestAuditLogRefusesUpdateAndDelete(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		ctx := context.Background()
		migrated(t, db)
		if err := db.InTx(ctx, func(tx *dbx.Tx) error { return insertAudit(ctx, tx, 1) }); err != nil {
			t.Fatalf("insert: %v", err)
		}
		for _, stmt := range []string{
			`UPDATE audit_log SET reason = 'changed' WHERE seq = 1`,
			`UPDATE audit_log SET hash = 'x'`,
			`DELETE FROM audit_log WHERE seq = 1`,
			`DELETE FROM audit_log`,
		} {
			err := db.InTx(ctx, func(tx *dbx.Tx) error {
				_, err := tx.ExecContext(ctx, stmt)
				return err
			})
			if err == nil || !strings.Contains(err.Error(), "append-only") {
				t.Errorf("%s: err = %v, want a refusal", stmt, err)
			}
		}
		if db.Dialect() == dbx.Postgres {
			if _, err := db.ExecContext(ctx, `TRUNCATE audit_log`); err == nil {
				t.Error("TRUNCATE succeeded")
			}
		}
		var reason string
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*), MAX(reason) FROM audit_log`).Scan(&n, &reason); err != nil || n != 1 || reason != "" {
			t.Errorf("after the attempts: %d rows, reason %q, %v", n, reason, err)
		}
		// Appending still works.
		if err := db.InTx(ctx, func(tx *dbx.Tx) error { return insertAudit(ctx, tx, 2) }); err != nil {
			t.Errorf("second insert: %v", err)
		}
	})
}

func TestForeignKeysAreEnforced(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		ctx := context.Background()
		migrated(t, db)
		_, err := db.ExecContext(ctx, `INSERT INTO memberships (product_id, user_id, role, created_at) VALUES (?, ?, 'member', 0)`, "no-product", "no-user")
		if err == nil {
			t.Error("a membership of a user and product that do not exist was accepted")
		}
	})
}

// prefix returns the first n migration files, as an older release had them.
func prefix(t *testing.T, n int) fs.FS {
	t.Helper()
	migs, err := dbx.LoadMigrations(migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, m := range migs[:n] {
		data, err := fs.ReadFile(migrations.FS(), m.Name)
		if err != nil {
			t.Fatal(err)
		}
		out[m.Name] = &fstest.MapFile{Data: data}
	}
	return out
}

// TestUpgradeFromEveryEarlierVersion migrates to each earlier release first
// and then to the current one; the result must be the same as migrating an
// empty database.
func TestUpgradeFromEveryEarlierVersion(t *testing.T) {
	migs, err := dbx.LoadMigrations(migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	fresh := migrated(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	for n := 1; n < len(migs); n++ {
		t.Run(migs[n-1].Name, func(t *testing.T) {
			db := dbxtest.Open(t, dbxtest.SQLiteURL(t))
			ctx := context.Background()
			if _, err := db.Migrate(ctx, migrations.Namespace, prefix(t, n)); err != nil {
				t.Fatal(err)
			}
			pending, err := migrations.Pending(ctx, db)
			if err != nil || len(pending) != len(migs)-n {
				t.Fatalf("pending %d, %v", len(pending), err)
			}
			if diff := dbx.Diff(fresh, migrated(t, db)); len(diff) > 0 {
				t.Errorf("upgraded schema differs from a fresh one:\n  %s", strings.Join(diff, "\n  "))
			}
		})
	}
}

// TestTwoServersStartTogether runs the real migrations from two handles at
// once on every available database.
func TestTwoServersStartTogether(t *testing.T) {
	for _, target := range dbxtest.Targets(t) {
		t.Run(target.Dialect.String(), func(t *testing.T) {
			a, b := dbxtest.Open(t, target.URL), dbxtest.Open(t, target.URL)
			ctx := context.Background()
			var wg sync.WaitGroup
			var appliedA, appliedB []dbx.Migration
			var errA, errB error
			wg.Add(2)
			go func() { defer wg.Done(); appliedA, errA = migrations.Apply(ctx, a) }()
			go func() { defer wg.Done(); appliedB, errB = migrations.Apply(ctx, b) }()
			wg.Wait()
			if errA != nil || errB != nil {
				t.Fatalf("errors: %v, %v", errA, errB)
			}
			all, _ := dbx.LoadMigrations(migrations.FS())
			if len(appliedA)+len(appliedB) != len(all) {
				t.Errorf("applied %d + %d migrations, want %d in total", len(appliedA), len(appliedB), len(all))
			}
		})
	}
}

func TestGoldenFileIsCommitted(t *testing.T) {
	if _, err := os.Stat(filepath.FromSlash(golden)); err != nil {
		t.Errorf("%s: %v", golden, err)
	}
}

// lineDiff shows which lines are only in want (-) or only in got (+).
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	inW, inG := map[string]int{}, map[string]int{}
	for _, l := range w {
		inW[l]++
	}
	for _, l := range g {
		inG[l]++
	}
	var b strings.Builder
	for _, l := range w {
		if inG[l] == 0 {
			b.WriteString("- " + l + "\n")
		}
	}
	for _, l := range g {
		if inW[l] == 0 {
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String()
}
