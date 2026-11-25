// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/config"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// openForCommand opens the database for an operations command, which works
// on a database that serve has set up: the migrations must have run. A
// SQLite file that is not there is reported, not created.
func openForCommand(ctx context.Context, e *env, name string, cfg *config.Config) (*dbx.DB, int) {
	if path, missing := sqliteFileMissing(cfg); missing {
		fmt.Fprintf(e.stderr, "%s %s: there is no database at %s; start the server first\n", brand.Server, name, path)
		return nil, exitFailure
	}
	db, err := openDatabase(ctx, cfg)
	if err != nil {
		fmt.Fprintf(e.stderr, "%s %s: %v\n", brand.Server, name, err)
		return nil, exitFailure
	}
	pending, err := migrations.Pending(ctx, db)
	if err == nil && len(pending) > 0 {
		err = fmt.Errorf("%d database migrations have not run; run %s migrate first", len(pending), brand.Server)
	}
	if err != nil {
		db.Close()
		fmt.Fprintf(e.stderr, "%s %s: %v\n", brand.Server, name, err)
		return nil, exitFailure
	}
	return db, exitOK
}

// runKeysRotate is "keys rotate": a new master key becomes current, the old
// ones stay in the key file as retired, and every sealed record's data key
// is rewrapped with the new key in one transaction. Nobody enters anything
// again. A server running next to it reloads the key file when it meets a
// record under the new key.
func runKeysRotate(ctx context.Context, e *env, args []string) int {
	var out string
	cfg, code := loadConfig("keys rotate", e, args, func(fs *flag.FlagSet) {
		fs.StringVar(&out, "out", "", "write the rotated keys to this new file instead of the current key file (required when the key is given in "+config.EnvName("MASTER_KEY")+")")
	})
	if cfg == nil {
		return code
	}
	db, code := openForCommand(ctx, e, "keys rotate", cfg)
	if db == nil {
		return code
	}
	defer db.Close()
	fail := func(err error) int {
		fmt.Fprintf(e.stderr, "%s keys rotate: %v\n", brand.Server, err)
		return exitFailure
	}

	keys, err := loadKeys(ctx, cfg, db, false)
	if err != nil {
		return fail(err)
	}
	// The key has to be the database's before anything is written.
	chainKey, err := settings.AuditKey(ctx, db, keys, false)
	if err != nil {
		return fail(err)
	}
	if keys.Source() == masterkey.FromEnv && out == "" {
		return fail(fmt.Errorf("the master key is given in %s, which this command cannot change; pass --out <new key file>, then start the server with %s pointing at it instead",
			config.EnvName("MASTER_KEY"), config.EnvName("MASTER_KEY_FILE")))
	}

	// The key file first: if the rewrap fails, the records are still under
	// the old key, which the new file keeps.
	rotated, err := keys.Rotate(out)
	if err != nil {
		return fail(err)
	}
	counts, err := masterkey.RewrapAll(ctx, db, rotated, rewrappers())
	if err != nil {
		return fail(fmt.Errorf("%w; the new key file %s keeps the old key as well, so everything still opens; run the command again", err, rotated.Path()))
	}
	total := 0
	var parts []string
	for name, n := range counts {
		total += n
		parts = append(parts, fmt.Sprintf("%s %d", name, n))
	}
	sort.Strings(parts)
	if chainKey != nil {
		log, err := audit.New(db, chainKey)
		if err == nil {
			_, err = log.Record(ctx, audit.Entry{Actor: audit.System, Action: "keys.rotate", Object: keyRotationObject(rotated),
				Change: map[string]any{"from": keys.CurrentID(), "to": rotated.CurrentID(), "rewrapped": total}})
		}
		if err != nil {
			return fail(fmt.Errorf("the key was rotated, but the audit entry could not be written: %w", err))
		}
	}

	fmt.Fprintf(e.stdout, "New master key %s is current; retired: %s.\n", rotated.CurrentID(), strings.Join(rotated.IDs()[1:], ", "))
	fmt.Fprintf(e.stdout, "Rewrapped %d sealed records (%s).\n", total, strings.Join(parts, ", "))
	fmt.Fprintf(e.stdout, "Key file: %s. Back it up now, apart from the database backups.\n", rotated.Path())
	if keys.Source() == masterkey.FromEnv {
		fmt.Fprintf(e.stdout, "Restart the server with %s=%s and without %s.\n", config.EnvName("MASTER_KEY_FILE"), rotated.Path(), config.EnvName("MASTER_KEY"))
	}
	return exitOK
}

// runAuditVerify is "audit verify": check the whole audit chain.
func runAuditVerify(ctx context.Context, e *env, args []string) int {
	cfg, code := loadConfig("audit verify", e, args, nil)
	if cfg == nil {
		return code
	}
	db, code := openForCommand(ctx, e, "audit verify", cfg)
	if db == nil {
		return code
	}
	defer db.Close()
	fail := func(err error) int {
		fmt.Fprintf(e.stderr, "%s audit verify: %v\n", brand.Server, err)
		return exitFailure
	}
	keys, err := loadKeys(ctx, cfg, db, false)
	if err != nil {
		return fail(err)
	}
	chainKey, err := settings.AuditKey(ctx, db, keys, false)
	if err != nil {
		return fail(err)
	}
	if chainKey == nil {
		fmt.Fprintln(e.stdout, "The audit log is empty: the server has not started on this database yet.")
		return exitOK
	}
	log, err := audit.New(db, chainKey)
	if err != nil {
		return fail(err)
	}
	rep, err := log.Verify(ctx, 1, "")
	if err != nil {
		return fail(err)
	}
	if rep.Broken != nil {
		fmt.Fprintf(e.stderr, "%s audit verify: the audit log was tampered with: %v (%d entries before it are intact)\n", brand.Server, rep.Broken, rep.Checked)
		return exitFailure
	}
	fmt.Fprintf(e.stdout, "The audit log is intact: %d entries, the last #%d.\n", rep.Checked, rep.LastSeq)
	return exitOK
}
