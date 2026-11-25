// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package dbx

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
)

// Migrations are .sql files named NNNN_name.sql, numbered from 0001 without
// gaps. They are append-only: once released, a file never changes (its
// checksum is recorded and checked on every start) and is never removed.
// There are no down migrations; going back means restoring a backup.
//
// A file is one transaction. Most of it is the portable subset that both
// databases run as written. What the subset cannot say goes into sections
// for one dialect:
//
//	-- +rgd dialect sqlite
//	CREATE TABLE blobs (… data BLOB NOT NULL …);
//	-- +rgd dialect postgres
//	CREATE TABLE blobs (… data BYTEA NOT NULL …);
//	-- +rgd end
//
// The core uses the namespace "core"; each optional module has its own.

// Migration is one migration file.
type Migration struct {
	Version  int
	Name     string // file name
	Checksum string // sha256 of the file with line endings normalized
	sqlite   string
	postgres string
}

// SQL returns the statements of the migration for a dialect.
func (m Migration) SQL(d Dialect) string {
	if d == Postgres {
		return m.postgres
	}
	return m.sqlite
}

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// directive is the comment prefix of the dialect sections.
var directive = "-- +" + brand.CLI + " "

// LoadMigrations reads the migration files at the top of fsys. Other files
// are an error, so that a misnamed migration is not silently skipped.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration %s: file names are NNNN_lower_case_name.sql", e.Name())
		}
		version, _ := strconv.Atoi(m[1])
		data, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		mig, err := parseMigration(e.Name(), version, data)
		if err != nil {
			return nil, err
		}
		out = append(out, mig)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, m := range out {
		if m.Version != i+1 {
			return nil, fmt.Errorf("migration %s: expected number %04d; migrations are numbered from 0001 without gaps or repeats", m.Name, i+1)
		}
	}
	return out, nil
}

func parseMigration(name string, version int, data []byte) (Migration, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	sum := sha256.Sum256([]byte(text))
	m := Migration{Version: version, Name: name, Checksum: hex.EncodeToString(sum[:])}

	var lite, pg strings.Builder
	section := "" // "", "sqlite" or "postgres"
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), directive); ok {
			switch fields := strings.Fields(rest); {
			case len(fields) == 2 && fields[0] == "dialect" && (fields[1] == "sqlite" || fields[1] == "postgres"):
				section = fields[1]
			case len(fields) == 1 && fields[0] == "end":
				if section == "" {
					return m, fmt.Errorf("migration %s:%d: end without a dialect section", name, lineNo)
				}
				section = ""
			default:
				return m, fmt.Errorf("migration %s:%d: unknown directive %q (want \"dialect sqlite\", \"dialect postgres\" or \"end\")", name, lineNo, rest)
			}
			// Keep line numbers of the database's error messages right.
			lite.WriteString("\n")
			pg.WriteString("\n")
			continue
		}
		if section != "postgres" {
			lite.WriteString(line + "\n")
		} else {
			lite.WriteString("\n")
		}
		if section != "sqlite" {
			pg.WriteString(line + "\n")
		} else {
			pg.WriteString("\n")
		}
	}
	if err := scanner.Err(); err != nil {
		return m, fmt.Errorf("migration %s: %w", name, err)
	}
	if section != "" {
		return m, fmt.Errorf("migration %s: the %s section is not closed with %send", name, section, directive)
	}
	m.sqlite, m.postgres = lite.String(), pg.String()
	return m, nil
}

// ErrNewerSchema means the database was migrated by a newer version of the
// program than this one. Running against a schema it does not know could
// damage data, so the program refuses (08-deployment.md#升级与迁移).
var ErrNewerSchema = errors.New("the database was migrated by a newer version of this program")

// ErrChangedMigration means a migration file differs from the one that was
// applied. Released migrations must never change.
var ErrChangedMigration = errors.New("a migration changed after it was applied")

const createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
    namespace  TEXT    NOT NULL,
    version    INTEGER NOT NULL,
    name       TEXT    NOT NULL,
    checksum   TEXT    NOT NULL,
    applied_at BIGINT  NOT NULL,
    PRIMARY KEY (namespace, version)
)`

// beforeMigrationTx is a test hook; see export_test.go.
var beforeMigrationTx func(version int)

// lockKey is what Tx.Lock serializes migrations on, across processes.
const lockKey = "schema_migrations"

type appliedRow struct {
	version  int
	name     string
	checksum string
}

// Migrate applies the migrations in fsys that the namespace has not had yet,
// in order, one transaction each, and returns the ones it applied. Two
// processes migrating the same database at once do not both run a migration:
// each transaction takes the migration lock first and then checks again what
// has been applied.
func (db *DB) Migrate(ctx context.Context, namespace string, fsys fs.FS) ([]Migration, error) {
	migs, err := LoadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	if err := db.InTx(ctx, func(tx *Tx) error {
		if err := tx.Lock(ctx, lockKey); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, createMigrationsTable)
		return err
	}); err != nil {
		return nil, fmt.Errorf("dbx: create schema_migrations: %w", err)
	}

	var applied []Migration
	for _, m := range migs {
		if beforeMigrationTx != nil {
			beforeMigrationTx(m.Version)
		}
		ran := false
		err := db.InTx(ctx, func(tx *Tx) error {
			if err := tx.Lock(ctx, lockKey); err != nil {
				return err
			}
			done, err := readApplied(ctx, tx, namespace)
			if err != nil {
				return err
			}
			if err := verifyApplied(namespace, done, migs); err != nil {
				return err
			}
			if _, ok := done[m.Version]; ok {
				return nil
			}
			if err := tx.execScript(ctx, m.SQL(db.dialect)); err != nil {
				return fmt.Errorf("dbx: migration %s/%s: %w", namespace, m.Name, err)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO schema_migrations (namespace, version, name, checksum, applied_at) VALUES (?, ?, ?, ?, ?)`,
				namespace, m.Version, m.Name, m.Checksum, Millis(time.Now())); err != nil {
				return fmt.Errorf("dbx: record migration %s/%s: %w", namespace, m.Name, err)
			}
			ran = true
			return nil
		})
		if err != nil {
			return applied, err
		}
		if ran {
			applied = append(applied, m)
		}
	}
	return applied, nil
}

// Pending returns the migrations in fsys that the namespace has not had yet.
// It returns ErrNewerSchema or ErrChangedMigration when the database does not
// match the files.
func (db *DB) Pending(ctx context.Context, namespace string, fsys fs.FS) ([]Migration, error) {
	migs, err := LoadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	exists, err := db.tableExists(ctx, "schema_migrations")
	if err != nil {
		return nil, err
	}
	done := map[int]appliedRow{}
	if exists {
		if done, err = readApplied(ctx, db, namespace); err != nil {
			return nil, err
		}
	}
	if err := verifyApplied(namespace, done, migs); err != nil {
		return nil, err
	}
	var pending []Migration
	for _, m := range migs {
		if _, ok := done[m.Version]; !ok {
			pending = append(pending, m)
		}
	}
	return pending, nil
}

// SchemaVersion returns the highest applied migration of the namespace, or 0.
func (db *DB) SchemaVersion(ctx context.Context, namespace string) (int, error) {
	exists, err := db.tableExists(ctx, "schema_migrations")
	if err != nil || !exists {
		return 0, err
	}
	var v sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations WHERE namespace = ?`, namespace).Scan(&v)
	return int(v.Int64), err
}

func (db *DB) tableExists(ctx context.Context, name string) (bool, error) {
	var q string
	switch db.dialect {
	case SQLite:
		q = `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`
	default:
		q = `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`
	}
	var n int
	if err := db.QueryRowContext(ctx, q, name).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func readApplied(ctx context.Context, q Querier, namespace string) (map[int]appliedRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT version, name, checksum FROM schema_migrations WHERE namespace = ?`, namespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]appliedRow{}
	for rows.Next() {
		var r appliedRow
		if err := rows.Scan(&r.version, &r.name, &r.checksum); err != nil {
			return nil, err
		}
		out[r.version] = r
	}
	return out, rows.Err()
}

func verifyApplied(namespace string, done map[int]appliedRow, migs []Migration) error {
	versions := make([]int, 0, len(done))
	for v := range done {
		versions = append(versions, v)
	}
	sort.Ints(versions)
	for _, v := range versions {
		r := done[v]
		if v > len(migs) {
			return fmt.Errorf("dbx: %w: %s has migration %d (%s), this program knows up to %d; run the newer version or restore a backup from before the upgrade",
				ErrNewerSchema, namespace, v, r.name, len(migs))
		}
		if m := migs[v-1]; m.Checksum != r.checksum {
			return fmt.Errorf("dbx: %w: %s/%s", ErrChangedMigration, namespace, path.Base(m.Name))
		}
	}
	return nil
}
