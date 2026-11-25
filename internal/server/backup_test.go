// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/config"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// The operations commands of docs/architecture/08-deployment.md#管理子命令 (M1-42).

const opsAdminPassword = "<placeholder: admin password>"

// opsServer is a data directory that a server has started on once: database migrated, master
// key generated, audit key in place.
type opsServer struct {
	t       *testing.T
	dataDir string
	vars    map[string]string
}

func newOpsServer(t *testing.T, vars map[string]string) *opsServer {
	t.Helper()
	e, _, _ := testEnv(t, vars)
	s := &opsServer{t: t, dataDir: e.lookupEnvValue(config.EnvName("DATA_DIR")), vars: map[string]string{}}
	for k, v := range vars {
		s.vars[k] = v
	}
	s.vars[config.EnvName("DATA_DIR")] = s.dataDir
	startServe(t, e).wait(t)
	return s
}

// env returns a command environment on the data directory, with more variables on top.
func (s *opsServer) env(more ...string) (*env, *syncBuffer, *syncBuffer) {
	vars := map[string]string{}
	for k, v := range s.vars {
		vars[k] = v
	}
	for i := 0; i+1 < len(more); i += 2 {
		vars[more[i]] = more[i+1]
	}
	return testEnv(s.t, vars)
}

// run runs a command on the data directory and returns its exit code and output.
func (s *opsServer) run(args ...string) (int, string, string) {
	e, out, errOut := s.env()
	code := runBounded(e, args...)
	return code, out.String(), errOut.String()
}

func (s *opsServer) dbPath() string { return filepath.Join(s.dataDir, brand.CLILong+".db") }

// open opens the database with the services that write to it the way the server does.
func (s *opsServer) open() (*dbx.DB, *authn.Service, *audit.Log) {
	s.t.Helper()
	ctx := context.Background()
	db := dbxtest.Open(s.t, "sqlite://"+filepath.ToSlash(s.dbPath()))
	keys, err := masterkey.Load(masterkey.LoadOptions{File: keyFile(s.dataDir)})
	if err != nil {
		s.t.Fatal(err)
	}
	chain, err := settings.AuditKey(ctx, db, keys, false)
	if err != nil {
		s.t.Fatal(err)
	}
	log, err := audit.New(db, chain)
	if err != nil {
		s.t.Fatal(err)
	}
	acc := access.New(db, access.Options{Auditor: accessAuditor{log}})
	return db, authn.New(db, authn.Options{Access: acc, Audit: log}), log
}

// createAdmin creates an administrator the way the setup wizard does.
func (s *opsServer) createAdmin(name string) {
	s.t.Helper()
	db, svc, _ := s.open()
	defer db.Close()
	ctx := context.Background()
	err := db.InTx(ctx, func(tx *dbx.Tx) error {
		_, err := svc.CreateFirstAdmin(ctx, tx, authn.FirstAdmin{Username: name, DisplayName: "Name of " + name, Password: opsAdminPassword})
		return err
	})
	if err != nil {
		s.t.Fatal(err)
	}
}

// dump is every row of every table, in a stable order, as text.
func dump(t *testing.T, path string) string {
	t.Helper()
	db, err := dbx.OpenSnapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	var b strings.Builder
	for _, table := range tables {
		fmt.Fprintf(&b, "== %s\n", table)
		rows, err := db.QueryContext(ctx, `SELECT * FROM "`+table+`" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%v\n", vals)
		}
		rows.Close()
	}
	return b.String()
}

var snapshotLine = regexp.MustCompile(`Snapshot written: (.+) \(`)

func snapshotPath(t *testing.T, out string) string {
	t.Helper()
	m := snapshotLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no snapshot in the output:\n%s", out)
	}
	return m[1]
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestBackupAndRestore is the acceptance check "after restoring from a snapshot the data is the
// same and the audit chain is intact": a backup, more changes (a key rotation among them), the
// restore done by the printed steps, and the server running on the restored database.
func TestBackupAndRestore(t *testing.T) {
	s := newOpsServer(t, nil)
	s.createAdmin("admin")
	before := dump(t, s.dbPath())

	out := t.TempDir()
	code, stdout, stderr := s.run("backup", "--out", out)
	if code != exitOK {
		t.Fatalf("backup: %d %s", code, stderr)
	}
	snap := snapshotPath(t, stdout)
	if filepath.Dir(snap) != out || !strings.Contains(stdout, "The master key is not in it") || !strings.Contains(stdout, keyFile(s.dataDir)) {
		t.Errorf("backup output:\n%s", stdout)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 1 {
		t.Errorf("the backup directory has %v; want the snapshot alone, without the master key", entries)
	}
	if got := dump(t, snap); got != before {
		t.Fatalf("the snapshot differs from the database:\n%s", lineDiffText(before, got))
	}

	// Life goes on: the key is rotated, a second administrator signs up.
	if code, _, stderr := s.run("keys", "rotate"); code != exitOK {
		t.Fatalf("rotate: %s", stderr)
	}
	db, svc, _ := s.open()
	ctx := context.Background()
	if err := db.InTx(ctx, func(tx *dbx.Tx) error {
		_, err := svc.CreateFirstAdmin(ctx, tx, authn.FirstAdmin{Username: "later", DisplayName: "Later", Password: opsAdminPassword})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	code, stdout, stderr = s.run("restore", "--check", snap)
	if code != exitOK || !strings.Contains(stdout, "Audit log: intact") || !strings.Contains(stdout, "The snapshot is sound") {
		t.Fatalf("restore --check: %d\n%s%s", code, stdout, stderr)
	}

	// The steps restore printed: copy over the database, drop -wal and -shm.
	copyFile(t, snap, s.dbPath())
	os.Remove(s.dbPath() + "-wal")
	os.Remove(s.dbPath() + "-shm")
	if got := dump(t, s.dbPath()); got != before {
		t.Errorf("the restored database differs from the one backed up:\n%s", lineDiffText(before, got))
	}
	if code, stdout, stderr := s.run("audit", "verify"); code != exitOK || !strings.Contains(stdout, "intact") {
		t.Errorf("audit verify after restoring: %d %s %s", code, stdout, stderr)
	}
	e, _, _ := s.env()
	srv := startServe(t, e)
	resp, body := get(t, "http://"+srv.addr+"/readyz")
	srv.wait(t)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"master_key":"ok"`) {
		t.Errorf("serving the restored database: %s %s", resp.Status, body)
	}
	db, svc, _ = s.open()
	defer db.Close()
	if _, err := svc.Login(ctx, "admin", opsAdminPassword); err != nil {
		t.Errorf("sign in after restoring: %v", err)
	}
	if _, err := svc.Login(ctx, "later", opsAdminPassword); !errors.Is(err, authn.ErrInvalidCredentials) {
		t.Errorf("the account made after the backup: %v", err)
	}
}

func lineDiffText(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out []string
	for _, l := range al {
		if !slices.Contains(bl, l) {
			out = append(out, "- "+l)
		}
	}
	for _, l := range bl {
		if !slices.Contains(al, l) {
			out = append(out, "+ "+l)
		}
	}
	return strings.Join(out, "\n")
}

// TestBackupWhileServing: the server need not stop.
func TestBackupWhileServing(t *testing.T) {
	s := newOpsServer(t, nil)
	e, _, _ := s.env()
	srv := startServe(t, e)
	code, stdout, stderr := s.run("backup")
	if code != exitOK {
		t.Fatalf("backup while serving: %d %s", code, stderr)
	}
	snap := snapshotPath(t, stdout)
	if filepath.Dir(snap) != filepath.Join(s.dataDir, "backups") {
		t.Errorf("default directory: %s", snap)
	}
	get(t, "http://"+srv.addr+"/readyz")
	srv.wait(t)
	if code, stdout, stderr := s.run("restore", "--check", snap); code != exitOK {
		t.Errorf("restore --check: %d %s %s", code, stdout, stderr)
	}
}

func TestBackupFailures(t *testing.T) {
	s := newOpsServer(t, nil)
	notADir := filepath.Join(t.TempDir(), "file")
	os.WriteFile(notADir, []byte("x"), 0o600)
	tests := []struct {
		name string
		vars []string
		args []string
		want string
	}{
		{"postgres", []string{config.EnvName("DATABASE_URL"), "postgres://app@db.example.com/rgd"}, nil, "pg_dump"},
		{"no database", []string{config.EnvName("DATA_DIR"), filepath.Join(t.TempDir(), "empty")}, nil, "there is no database"},
		{"out is a file", nil, []string{"--out", notADir}, "backup directory"},
		{"stray argument", nil, []string{"now"}, "unexpected argument"},
	}
	for _, tt := range tests {
		e, stdout, stderr := s.env(tt.vars...)
		code := runBounded(e, append([]string{"backup"}, tt.args...)...)
		if code == exitOK || !strings.Contains(stderr.String(), tt.want) || strings.Contains(stdout.String(), "Snapshot written") {
			t.Errorf("%s: %d %q", tt.name, code, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "empty")); err == nil {
		t.Error("backup created a data directory")
	}
}

// TestRestoreCheckFailures: every way a snapshot can be unusable is caught before anyone stops
// a server for it.
func TestRestoreCheckFailures(t *testing.T) {
	s := newOpsServer(t, nil)
	s.createAdmin("admin")
	code, stdout, _ := s.run("backup", "--out", t.TempDir())
	if code != exitOK {
		t.Fatal(stdout)
	}
	good := snapshotPath(t, stdout)
	dir := t.TempDir()

	junk := filepath.Join(dir, "junk.db")
	os.WriteFile(junk, []byte(strings.Repeat("not a database ", 100)), 0o600)

	tampered := filepath.Join(dir, "tampered.db")
	copyFile(t, good, tampered)
	editSQLite(t, tampered, `DROP TRIGGER audit_log_no_update`, `UPDATE audit_log SET object_label = 'nothing happened' WHERE seq = 1`)

	newer := filepath.Join(dir, "newer.db")
	copyFile(t, good, newer)
	editSQLite(t, newer, `INSERT INTO schema_migrations (namespace, version, name, checksum, applied_at) VALUES ('core', 9999, '9999_future.sql', 'x', 0)`)

	foreign := filepath.Join(dir, "foreign.db")
	editSQLite(t, foreign, `CREATE TABLE notes (body TEXT)`)

	otherKey := filepath.Join(dir, "other.key")
	if err := writeKeyFile(otherKey); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		file string
		vars []string
		want string
	}{
		{"missing", filepath.Join(dir, "missing.db"), nil, "no such file"},
		{"not a database", junk, nil, "not a SQLite database"},
		{"audit tampered with", tampered, nil, "tampered with"},
		{"newer schema", newer, nil, "newer than this server"},
		{"not this server's", foreign, nil, "none of this server's tables"},
		{"another master key", good, []string{config.EnvName("MASTER_KEY_FILE"), otherKey}, "does not open this snapshot"},
		{"master key missing", good, []string{config.EnvName("MASTER_KEY_FILE"), filepath.Join(dir, "lost.key")}, "restore the key file"},
	}
	for _, tt := range tests {
		e, stdout, stderr := s.env(tt.vars...)
		code := runBounded(e, "restore", "--check", tt.file)
		if code != exitFailure || !strings.Contains(stderr.String(), tt.want) || strings.Contains(stdout.String(), "sound") {
			t.Errorf("%s: %d\n%s%s", tt.name, code, stdout, stderr)
		}
	}
	if code, _, stderr := s.run("restore", good); code != exitUsage || stderr == "" {
		t.Errorf("restore without --check: %d %s", code, stderr)
	}
	if code, _, stderr := s.run("restore"); code != exitUsage || !strings.Contains(stderr, "stop the server") {
		t.Errorf("restore alone: %d %s", code, stderr)
	}
}

// editSQLite changes (or creates) a SQLite file directly, as someone with the file would.
func editSQLite(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// partlyMigrated makes a database in dataDir with the first n core migrations only, as an
// older version of the server left it.
func partlyMigrated(t *testing.T, dataDir string, n int) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	all, err := dbx.LoadMigrations(migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	older := fstest.MapFS{}
	for _, m := range all[:n] {
		data, err := fsReadFile(m.Name)
		if err != nil {
			t.Fatal(err)
		}
		older[m.Name] = &fstest.MapFile{Data: data}
	}
	db := dbxtest.Open(t, "sqlite://"+filepath.ToSlash(filepath.Join(dataDir, brand.CLILong+".db")))
	if _, err := db.Migrate(context.Background(), migrations.Namespace, older); err != nil {
		t.Fatal(err)
	}
	db.Close()
}

func fsReadFile(name string) ([]byte, error) {
	f, err := migrations.FS().Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// TestSnapshotBeforeMigrating: a database with data that is about to be migrated is
// snapshotted first, by migrate and by serve; the snapshot is the database as it was. A new
// database is not, and a snapshot that fails stops the migrations.
func TestSnapshotBeforeMigrating(t *testing.T) {
	all, err := dbx.LoadMigrations(migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	last := all[len(all)-1].Version
	for _, cmd := range []string{"migrate", "serve"} {
		t.Run(cmd, func(t *testing.T) {
			e, stdout, stderr := testEnv(t, nil)
			dataDir := e.lookupEnvValue(config.EnvName("DATA_DIR"))
			partlyMigrated(t, dataDir, 5)
			before := dump(t, filepath.Join(dataDir, brand.CLILong+".db"))
			if cmd == "serve" {
				startServe(t, e).wait(t)
			} else if code := runBounded(e, "migrate"); code != exitOK {
				t.Fatalf("migrate: %d %s %s", code, stdout, stderr)
			}
			matches, _ := filepath.Glob(filepath.Join(dataDir, "backups", fmt.Sprintf("pre-migrate-0005-%04d-*.db", last)))
			if len(matches) != 1 {
				t.Fatalf("snapshots: %v\n%s", matches, stdout)
			}
			if got := dump(t, matches[0]); got != before {
				t.Errorf("the snapshot is not the database before migrating:\n%s", lineDiffText(before, got))
			}
			if !strings.Contains(stdout.String(), "snapshot before migrating") {
				t.Errorf("not in the log:\n%s", stdout)
			}
		})
	}

	t.Run("new database", func(t *testing.T) {
		e, _, _ := testEnv(t, nil)
		startServe(t, e).wait(t)
		if _, err := os.Stat(filepath.Join(e.lookupEnvValue(config.EnvName("DATA_DIR")), "backups")); err == nil {
			t.Error("a new database was snapshotted")
		}
	})

	t.Run("snapshot fails", func(t *testing.T) {
		e, stdout, _ := testEnv(t, nil)
		dataDir := e.lookupEnvValue(config.EnvName("DATA_DIR"))
		partlyMigrated(t, dataDir, 5)
		// Where the snapshots go is taken by a file.
		os.WriteFile(filepath.Join(dataDir, "backups"), []byte("x"), 0o600)
		if code := runBounded(e, "migrate"); code != exitFailure || !strings.Contains(stdout.String(), "the migrations did not run") {
			t.Errorf("migrate: %d\n%s", code, stdout)
		}
		db := dbxtest.Open(t, "sqlite://"+filepath.ToSlash(filepath.Join(dataDir, brand.CLILong+".db")))
		if v, _ := db.SchemaVersion(context.Background(), migrations.Namespace); v != 5 {
			t.Errorf("schema version %d after a failed snapshot, want 5", v)
		}
	})
}

// TestDailyBackup: with backup.dir the server makes a backup when it starts (the job is due)
// and keeps backup.keep of them; files it did not write stay.
func TestDailyBackup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	os.MkdirAll(dir, 0o700)
	old := []string{brand.CLILong + "-20260101T000000Z.db", brand.CLILong + "-20260102T000000Z.db", brand.CLILong + "-20260103T000000Z.db"}
	for _, n := range append(old, "pre-migrate-0001-0002-20260101T000000Z.db", "notes.txt") {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600)
	}
	e, _, _ := testEnv(t, map[string]string{config.EnvName("BACKUP_DIR"): dir, config.EnvName("BACKUP_KEEP"): "2"})
	srv := startServe(t, e)
	deadline := time.Now().Add(20 * time.Second)
	var names []string
	for {
		entries, _ := os.ReadDir(dir)
		names = names[:0]
		for _, en := range entries {
			names = append(names, en.Name())
		}
		if !slices.Contains(names, old[0]) && !slices.Contains(names, old[1]) || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	resp, body := get(t, "http://"+srv.addr+"/readyz")
	srv.wait(t)
	var fresh string
	for _, n := range names {
		if dailyBackupName.MatchString(n) && !slices.Contains(old, n) {
			fresh = n
		}
	}
	want := []string{old[2], "notes.txt", "pre-migrate-0001-0002-20260101T000000Z.db", fresh}
	slices.Sort(want)
	slices.Sort(names)
	if fresh == "" || !slices.Equal(names, want) {
		t.Errorf("backup directory: %v, want %v", names, want)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"backup":"ok"`) {
		t.Errorf("readyz: %s", body)
	}
	if fresh != "" {
		if code, stdout, stderr := (&opsServer{t: t, vars: map[string]string{config.EnvName("DATA_DIR"): e.lookupEnvValue(config.EnvName("DATA_DIR"))}}).
			run("restore", "--check", filepath.Join(dir, fresh)); code != exitOK {
			t.Errorf("the daily backup does not check: %s %s", stdout, stderr)
		}
	}
}
