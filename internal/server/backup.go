// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/config"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/jobs"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// Backups (docs/architecture/08-deployment.md#备份与恢复). Two things need backing up, and
// they are kept apart: the database and the master key.
//
//   - "backup" writes a consistent snapshot of a SQLite database (VACUUM INTO, the server keeps
//     running) to the backup directory. PostgreSQL is backed up with its own tools.
//   - The snapshot never contains the master key, on purpose (06-server.md#密钥加密): the key is
//     what makes a stolen database useless. The command says where the key is and that its copy
//     belongs somewhere else.
//   - With backup.dir set the server makes one every day (the job "backup") and keeps the
//     newest backup.keep of them.
//   - Before migrating a SQLite database that already has data, serve and migrate make a
//     snapshot named pre-migrate-<schema version before>-<after>-<time>.db.
//   - "restore --check <snapshot>" opens a snapshot read-only and checks that it is a sound
//     database, which schema version it has, that the master key opens it and that its audit
//     chain is intact. Restoring is done by hand with the server stopped; the command prints how.

// backupJob is the daily backup's job name.
const backupJob = "backup"

const (
	backupEvery   = 24 * time.Hour
	backupTimeout = time.Hour
)

// backupStamp is the time in backup file names: UTC, sortable, no characters a file system
// minds.
const backupStamp = "20060102T150405Z"

// dailyBackupName matches the files the daily job and the backup command write, and only
// those: pruning must not touch anything else in the directory.
var dailyBackupName = regexp.MustCompile(`^` + regexp.QuoteMeta(brand.CLILong) + `-\d{8}T\d{6}Z\.db$`)

// backupDir is where backups go: backup.dir when set, else backups/ in the data directory.
func backupDir(cfg *config.Config) string {
	if cfg.BackupDir != "" {
		return cfg.BackupDir
	}
	return filepath.Join(cfg.DataDir, "backups")
}

// snapshotTo writes a snapshot of db into dir under name; the directory is made (0700) when
// missing.
func snapshotTo(ctx context.Context, db *dbx.DB, dir, name string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("the backup directory: %w", err)
	}
	path := filepath.Join(dir, name)
	if err := db.Snapshot(ctx, path); err != nil {
		return "", err
	}
	return path, nil
}

// sqliteFileMissing reports whether the configuration names a SQLite database whose file does
// not exist: commands that read an existing database say so instead of creating an empty one.
func sqliteFileMissing(cfg *config.Config) (string, bool) {
	if cfg.DatabaseScheme() != config.SchemeSQLite {
		return "", false
	}
	path := filepath.FromSlash(strings.TrimPrefix(cfg.DatabaseURL, config.SchemeSQLite+"://"))
	_, err := os.Stat(path)
	return path, errors.Is(err, fs.ErrNotExist)
}

// masterKeyWhere says where the master key is, for the reminder that it is not in a backup.
func masterKeyWhere(cfg *config.Config) string {
	if cfg.MasterKey != nil {
		return "the value of " + config.EnvName("MASTER_KEY")
	}
	return cfg.MasterKeyFile
}

// runBackup is "backup": a snapshot of the SQLite database, made while the server runs.
func runBackup(ctx context.Context, e *env, args []string) int {
	var out string
	cfg, code := loadConfig("backup", e, args, func(fs *flag.FlagSet) {
		fs.StringVar(&out, "out", "", "directory to write the snapshot to (default backup.dir, else <data dir>/backups)")
	})
	if cfg == nil {
		return code
	}
	fail := func(format string, args ...any) int {
		fmt.Fprintf(e.stderr, "%s backup: %s\n", brand.Server, fmt.Sprintf(format, args...))
		return exitFailure
	}
	if cfg.DatabaseScheme() != config.SchemeSQLite {
		return fail("the database is PostgreSQL, which this command does not back up: use pg_dump (or your provider's snapshots), and keep a copy of the master key (%s) apart from it", masterKeyWhere(cfg))
	}
	if path, missing := sqliteFileMissing(cfg); missing {
		return fail("there is no database at %s", path)
	}
	db, err := openDatabase(ctx, cfg)
	if err != nil {
		return fail("%v", err)
	}
	defer db.Close()
	dir := out
	if dir == "" {
		dir = backupDir(cfg)
	}
	path, err := snapshotTo(ctx, db, dir, brand.CLILong+"-"+time.Now().UTC().Format(backupStamp)+".db")
	if err != nil {
		return fail("%v", err)
	}
	version, _ := db.SchemaVersion(ctx, migrations.Namespace)
	size := int64(0)
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	}
	fmt.Fprintf(e.stdout, "Snapshot written: %s (%s, schema version %d).\n", path, humanBytes(size), version)
	fmt.Fprintf(e.stdout, "It holds encrypted data and all metadata: keep it like other sensitive data.\n")
	fmt.Fprintf(e.stdout, "The master key is not in it. Keep a copy of %s as well, apart from the database backups:\n", masterKeyWhere(cfg))
	fmt.Fprintf(e.stdout, "without the key nothing encrypted can be read; with both, everything can.\n")
	fmt.Fprintf(e.stdout, "Check it with: %s restore --check %s\n", brand.Server, path)
	return exitOK
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// restoreSteps is how to restore a snapshot, printed by restore.
func restoreSteps(cfg *config.Config, snapshot string) string {
	db := filepath.FromSlash(strings.TrimPrefix(cfg.DatabaseURL, config.SchemeSQLite+"://"))
	if cfg.DatabaseScheme() != config.SchemeSQLite {
		db = filepath.Join(cfg.DataDir, brand.CLILong+".db")
	}
	return fmt.Sprintf(`To restore it:
  1. stop the server;
  2. copy %[1]s to %[2]s;
  3. delete %[2]s-wal and %[2]s-shm if they are there;
  4. put the master key back in place if it was lost (%[3]s);
  5. start the server.
`, snapshot, db, masterKeyWhere(cfg))
}

// runRestore is "restore --check <snapshot>": check a snapshot without touching the data in
// use. Restoring itself is a few steps by hand with the server stopped, printed at the end; a
// command that replaces the database of a server that may be running is not offered.
func runRestore(ctx context.Context, e *env, args []string) int {
	var check string
	cfg, code := loadConfig("restore", e, args, func(fs *flag.FlagSet) {
		fs.StringVar(&check, "check", "", "the snapshot to check (required)")
	})
	if cfg == nil {
		return code
	}
	if check == "" {
		fmt.Fprintf(e.stderr, "%s restore: give the snapshot to check with --check <file>. This command only checks;\n%s", brand.Server,
			restoreSteps(cfg, "<snapshot>"))
		return exitUsage
	}
	rep, err := checkSnapshot(ctx, cfg, check)
	for _, line := range rep.lines {
		fmt.Fprintln(e.stdout, line)
	}
	if err != nil {
		fmt.Fprintf(e.stderr, "%s restore --check: %v\n", brand.Server, err)
		return exitFailure
	}
	fmt.Fprintf(e.stdout, "The snapshot is sound.\n%s", restoreSteps(cfg, check))
	return exitOK
}

type snapshotReport struct{ lines []string }

func (r *snapshotReport) add(format string, args ...any) {
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

// checkSnapshot checks a snapshot: it is a SQLite database that passes the integrity check, its
// schema is one this server can run (now or after migrating), the configured master key opens
// it, and its audit chain is intact. The snapshot is opened read-only and is not changed.
func checkSnapshot(ctx context.Context, cfg *config.Config, path string) (snapshotReport, error) {
	var rep snapshotReport
	snap, err := dbx.OpenSnapshot(ctx, path)
	if err != nil {
		return rep, err
	}
	defer snap.Close()
	if info, err := os.Stat(path); err == nil {
		rep.add("Snapshot: %s (%s, written %s)", path, humanBytes(info.Size()), info.ModTime().UTC().Format(time.RFC3339))
	}
	problems, err := snap.IntegrityCheck(ctx)
	if err != nil {
		return rep, fmt.Errorf("the integrity check did not run: %w", err)
	}
	if len(problems) > 0 {
		if len(problems) > 5 {
			problems = append(problems[:5], "…")
		}
		return rep, fmt.Errorf("the snapshot is damaged: %s", strings.Join(problems, "; "))
	}
	rep.add("Integrity: ok")

	version, err := snap.SchemaVersion(ctx, migrations.Namespace)
	if err != nil {
		return rep, fmt.Errorf("the schema version: %w", err)
	}
	if version == 0 {
		return rep, errors.New("the snapshot has none of this server's tables: it is not a backup of this server")
	}
	pending, err := migrations.Pending(ctx, snap)
	switch {
	case errors.Is(err, dbx.ErrNewerSchema):
		return rep, fmt.Errorf("schema version %d is newer than this server knows: restore it with the version of %s that made it (%w)", version, brand.Server, err)
	case err != nil:
		return rep, err
	case len(pending) > 0:
		rep.add("Schema version: %d; this server migrates it on start (%d migrations)", version, len(pending))
	default:
		rep.add("Schema version: %d, the current one", version)
	}

	sealed, err := settings.HasSealedData(ctx, snap)
	if err != nil {
		return rep, err
	}
	keys, err := loadKeys(ctx, cfg, snap, false)
	if errors.Is(err, masterkey.ErrNoKeyFile) && !sealed {
		rep.add("Master key: none at %s, and the snapshot holds nothing encrypted", cfg.MasterKeyFile)
		keys = nil
	} else if err != nil {
		return rep, fmt.Errorf("the master key: %w", err)
	}
	var chainKey []byte
	if keys != nil {
		if chainKey, err = settings.AuditKey(ctx, snap, keys, false); err != nil {
			return rep, fmt.Errorf("the master key (%s) does not open this snapshot: %w", masterKeyWhere(cfg), err)
		}
		rep.add("Master key: %s opens it", keys.Source())
	}
	if chainKey == nil {
		rep.add("Audit log: empty (the server had not started on this database)")
		return rep, nil
	}
	log, err := audit.New(snap, chainKey)
	if err != nil {
		return rep, err
	}
	v, err := log.Verify(ctx, 1, "")
	if err != nil {
		return rep, fmt.Errorf("the audit log: %w", err)
	}
	if v.Broken != nil {
		return rep, fmt.Errorf("the audit log in the snapshot was tampered with: %v (%d entries before it are intact)", v.Broken, v.Checked)
	}
	rep.add("Audit log: intact, %d entries, the last #%d", v.Checked, v.LastSeq)
	return rep, nil
}

// beforeMigrating is called with the pending migrations before they are applied, by serve and
// by migrate. On SQLite with data in it, it snapshots the database first, and the migrations do
// not run when that fails. On PostgreSQL it only says to back up first; the operator does that
// (RGD_AUTO_MIGRATE=false, back up, then migrate).
func beforeMigrating(ctx context.Context, cfg *config.Config, db *dbx.DB, log *slog.Logger, pending []dbx.Migration) error {
	if len(pending) == 0 {
		return nil
	}
	from, err := db.SchemaVersion(ctx, migrations.Namespace)
	if err != nil {
		return err
	}
	if from == 0 {
		return nil // a new database: nothing to keep
	}
	to := pending[len(pending)-1].Version
	if db.Dialect() != dbx.SQLite {
		log.Warn("migrating the PostgreSQL database; this server does not back PostgreSQL up: make sure you have a backup from before this start",
			"from", from, "to", to, "hint", "set "+config.EnvName("AUTO_MIGRATE")+"=false to run "+brand.Server+" migrate yourself after the backup")
		return nil
	}
	name := fmt.Sprintf("pre-migrate-%04d-%04d-%s.db", from, to, time.Now().UTC().Format(backupStamp))
	path, err := snapshotTo(ctx, db, backupDir(cfg), name)
	if err != nil {
		return fmt.Errorf("the snapshot before migrating failed, so the migrations did not run: %w", err)
	}
	log.Info("snapshot before migrating", "file", path, "from", from, "to", to)
	return nil
}

// migrate applies the pending core migrations after beforeMigrating.
func migrate(ctx context.Context, cfg *config.Config, db *dbx.DB, log *slog.Logger) ([]dbx.Migration, error) {
	pending, err := migrations.Pending(ctx, db)
	if err != nil {
		return nil, err
	}
	if err := beforeMigrating(ctx, cfg, db, log, pending); err != nil {
		return nil, err
	}
	return migrations.Apply(ctx, db)
}

// dailyBackup is the job: a snapshot into backup.dir, then the oldest ones beyond backup.keep
// are deleted.
func (a *App) dailyBackup(ctx context.Context) error {
	dir := a.cfg.BackupDir
	path, err := snapshotTo(ctx, a.db, dir, brand.CLILong+"-"+time.Now().UTC().Format(backupStamp)+".db")
	if err != nil {
		return err
	}
	removed, err := pruneBackups(dir, a.cfg.BackupKeep)
	a.log.Info("backup written", "file", path, "removed", len(removed))
	return err
}

// pruneBackups deletes the oldest of the backups in dir (by the time in their names) so that
// keep of them remain, and returns what it deleted. Files it did not write are left alone.
func pruneBackups(dir string, keep int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && dailyBackupName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	var removed []string
	var errs []error
	for len(names) > keep {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			errs = append(errs, err)
		} else {
			removed = append(removed, names[0])
		}
		names = names[1:]
	}
	return removed, errors.Join(errs...)
}

// addBackupJob schedules the daily backup when backup.dir is set, and its readiness item.
func (a *App) addBackupJob() error {
	if a.cfg.BackupDir == "" {
		return nil
	}
	if a.db.Dialect() != dbx.SQLite {
		a.log.Warn("backup.dir is for SQLite; with PostgreSQL it is ignored: back the database up with pg_dump or your provider's snapshots")
		return nil
	}
	if err := a.bg.Jobs.Add(jobs.Job{Name: backupJob, Every: backupEvery, Timeout: backupTimeout, Run: a.dailyBackup}); err != nil {
		return err
	}
	a.AddReadyCheck(ReadyCheck{Name: "backup", Check: func(ctx context.Context) error {
		statuses, err := a.bg.Jobs.Statuses(ctx)
		if err != nil {
			return err
		}
		for _, s := range statuses {
			if s.Name == backupJob && (s.LastResult == jobs.Failed || s.LastResult == jobs.TimedOut) {
				return fmt.Errorf("the last backup did not finish (%s): %s", s.LastResult, s.LastError)
			}
		}
		return nil
	}})
	return nil
}
