// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/config"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// runDoctor is "doctor" (docs/architecture/06-server.md#可观测性): check the server's
// environment and say, item by item, what is wrong and what to do. It looks and does not fix:
// it creates no data directory, database or key. Exit 1 when an item fails; warnings do not
// change the exit code.
func runDoctor(ctx context.Context, e *env, args []string) int {
	cfg, code := loadConfig("doctor", e, args, nil)
	if cfg == nil {
		return code
	}
	d := &doctor{cfg: cfg, now: time.Now()}
	if e.doctorDial != nil {
		d.dial = e.doctorDial
	}
	if !e.doctorNow.IsZero() {
		d.now = e.doctorNow
	}
	results := d.run(ctx)
	failed := 0
	for _, r := range results {
		fmt.Fprintf(e.stdout, "%-5s %-16s %s\n", r.status, r.name, r.detail)
		if r.status == statusFail {
			failed++
		}
	}
	if failed > 0 {
		fmt.Fprintf(e.stdout, "\n%d of %d checks failed.\n", failed, len(results))
		return exitFailure
	}
	fmt.Fprintf(e.stdout, "\nNo check failed.\n")
	return exitOK
}

// The states of a doctor item.
const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
	statusSkip = "-"
)

type result struct{ status, name, detail string }

// Thresholds of the doctor.
const (
	diskFail = 100 << 20 // free bytes below which the data directory's disk fails
	diskWarn = 1 << 30   // and below which it warns
	// certWarn is how long before its end a certificate given in files warns. Automatic ones
	// are renewed 30 days before; acmeWarn left means the renewal is overdue.
	certWarn    = 14 * 24 * time.Hour
	acmeWarn    = 20 * 24 * time.Hour
	backupStale = 50 * time.Hour // a daily backup older than this is late
	mailTimeout = 5 * time.Second
)

type doctor struct {
	cfg  *config.Config
	now  time.Time
	dial func(ctx context.Context, network, addr string) (net.Conn, error)

	out []result
	// What later items need from earlier ones; nil or false when not available.
	db     *dbx.DB
	keys   *masterkey.Keyring
	chain  []byte
	sealed bool
	// dbReady: the database is there and fully migrated, so its settings can be read.
	dbReady bool
}

func (d *doctor) add(status, name, format string, args ...any) {
	d.out = append(d.out, result{status, name, fmt.Sprintf(format, args...)})
}

func (d *doctor) run(ctx context.Context) []result {
	d.checkConfig()
	d.checkDataDir()
	d.checkDatabase(ctx)
	if d.db != nil {
		defer d.db.Close()
	}
	d.checkMigrations(ctx)
	d.checkMasterKey(ctx)
	d.checkAudit(ctx)
	d.checkMail(ctx)
	d.checkCertificate()
	d.checkDisk()
	d.checkGit(ctx)
	d.checkBackups()
	return d.out
}

func (d *doctor) checkConfig() {
	var problems, notes []string
	for _, f := range configFindings(d.cfg) {
		if f.level == levelProblem {
			problems = append(problems, f.text)
		} else {
			notes = append(notes, f.text)
		}
	}
	switch {
	case len(problems) > 0:
		d.add(statusFail, "config", "%s (run %s config check for all of it)", strings.Join(problems, "; "), brand.Server)
	case len(notes) > 0:
		d.add(statusOK, "config", "consistent; %d notes in %s config check", len(notes), brand.Server)
	default:
		d.add(statusOK, "config", "consistent")
	}
}

func (d *doctor) checkDataDir() {
	dir := d.cfg.DataDir
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		d.add(statusWarn, "data directory", "%s does not exist yet; serve creates it (0700) on first start", dir)
		return
	case err != nil:
		d.add(statusFail, "data directory", "%v", err)
		return
	case !info.IsDir():
		d.add(statusFail, "data directory", "%s is not a directory", dir)
		return
	}
	// Writable: what the server does first. The probe file is removed at once.
	probe, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		d.add(statusFail, "data directory", "%s is not writable by this user: %v", dir, err)
		return
	}
	probe.Close()
	os.Remove(probe.Name())
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			d.add(statusWarn, "data directory", "%s is open to other users (mode %04o): it holds the database and the master key; chmod 0700 it", dir, perm)
			return
		}
		if owner, ok := fileOwner(info); ok && owner != os.Geteuid() {
			d.add(statusWarn, "data directory", "%s belongs to user %d, but this runs as %d: run the doctor as the server's user", dir, owner, os.Geteuid())
			return
		}
	}
	d.add(statusOK, "data directory", "%s, writable, not open to other users", dir)
}

func (d *doctor) checkDatabase(ctx context.Context) {
	if path, missing := sqliteFileMissing(d.cfg); missing {
		d.add(statusWarn, "database", "no database at %s yet; serve creates it on first start", path)
		return
	}
	db, err := dbx.Open(ctx, d.cfg.DatabaseURL)
	if err != nil {
		d.add(statusFail, "database", "%v", err)
		return
	}
	if err := db.Ping(ctx); err != nil {
		db.Close()
		d.add(statusFail, "database", "%v", err)
		return
	}
	d.db = db
	if p := db.Path(); p != "" {
		size := ""
		if info, err := os.Stat(p); err == nil {
			size = ", " + humanBytes(info.Size())
		}
		d.add(statusOK, "database", "SQLite %s%s", p, size)
		return
	}
	d.add(statusOK, "database", "PostgreSQL, reachable")
}

func (d *doctor) checkMigrations(ctx context.Context) {
	if d.db == nil {
		d.add(statusSkip, "migrations", "no database")
		return
	}
	version, _ := d.db.SchemaVersion(ctx, migrations.Namespace)
	pending, err := migrations.Pending(ctx, d.db)
	switch {
	case errors.Is(err, dbx.ErrNewerSchema):
		d.add(statusFail, "migrations", "the database was migrated by a newer %s (schema version %d): run that version", brand.Server, version)
	case err != nil:
		d.add(statusFail, "migrations", "%v", err)
	case len(pending) == 0:
		d.add(statusOK, "migrations", "schema version %d, up to date", version)
	case d.cfg.AutoMigrate:
		d.add(statusWarn, "migrations", "%d migrations run at the next start (from schema version %d)", len(pending), version)
	default:
		d.add(statusFail, "migrations", "%d migrations have not run and auto_migrate is off: back up, then run %s migrate", len(pending), brand.Server)
	}
	d.dbReady = err == nil && len(pending) == 0
}

func (d *doctor) checkMasterKey(ctx context.Context) {
	name := "master key"
	if d.db != nil {
		sealed, err := settings.HasSealedData(ctx, d.db)
		if err != nil {
			d.add(statusFail, name, "%v", err)
			return
		}
		d.sealed = sealed
	}
	keys, err := masterkey.Load(masterkey.LoadOptions{Key: d.cfg.MasterKey, File: d.cfg.MasterKeyFile})
	switch {
	case errors.Is(err, masterkey.ErrNoKeyFile) && !d.sealed:
		d.add(statusWarn, name, "no key at %s yet; serve generates it on first start, then back it up apart from the database", d.cfg.MasterKeyFile)
		return
	case errors.Is(err, masterkey.ErrNoKeyFile):
		d.add(statusFail, name, "%s is missing, and the database holds data encrypted with it: restore the key file from your backup", d.cfg.MasterKeyFile)
		return
	case err != nil:
		d.add(statusFail, name, "%v", err)
		return
	}
	if d.db != nil && d.dbReady {
		chain, err := settings.AuditKey(ctx, d.db, keys, false)
		if err != nil {
			d.add(statusFail, name, "the key from %s does not open this database: %v", keys.Source(), err)
			return
		}
		d.chain = chain
	}
	d.keys = keys
	where := keys.Source().String()
	if keys.Source() != masterkey.FromEnv {
		where = keys.Path()
		if info, err := os.Stat(keys.Path()); err == nil && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			d.add(statusWarn, name, "%s is open to other users (mode %04o): chmod 0600 it", keys.Path(), info.Mode().Perm())
			return
		}
	}
	detail := fmt.Sprintf("key %s from %s", keys.CurrentID(), where)
	if keys.Source() != masterkey.FromEnv && within(keys.Path(), d.cfg.DataDir) {
		detail += "; it is in the data directory, so back it up apart from the database"
	}
	d.add(statusOK, name, "%s", detail)
}

func (d *doctor) checkAudit(ctx context.Context) {
	if d.chain == nil {
		d.add(statusSkip, "audit log", "nothing to check yet")
		return
	}
	s := settings.New(d.db, d.keys, nil, settings.Options{})
	c, found, err := s.AuditCheck(ctx)
	switch {
	case err != nil:
		d.add(statusFail, "audit log", "%v", err)
	case !found:
		d.add(statusOK, "audit log", "not checked yet; the server checks it every day, or run %s audit verify", brand.Server)
	case c.Broken != "":
		d.add(statusFail, "audit log", "tampered with at #%d (%s), found %s: restore from a backup; %s audit verify shows the state",
			c.BrokenSeq, c.Broken, c.At.Format(time.RFC3339), brand.Server)
	default:
		d.add(statusOK, "audit log", "intact through #%d at the check of %s", c.LastSeq, c.At.Format(time.RFC3339))
	}
}

func (d *doctor) checkMail(ctx context.Context) {
	if d.chain == nil {
		d.add(statusSkip, "mail", "no settings yet")
		return
	}
	log, err := audit.New(d.db, d.chain)
	if err != nil {
		d.add(statusFail, "mail", "%v", err)
		return
	}
	m, err := settings.New(d.db, d.keys, log, settings.Options{}).MailServer(ctx)
	switch {
	case err != nil:
		d.add(statusFail, "mail", "the mail settings do not open: %v", err)
		return
	case !m.Enabled || m.Host == "":
		d.add(statusWarn, "mail", "off: invitations and notifications are not sent by mail (platform settings)")
		return
	}
	addr := net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
	dial := d.dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	dctx, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	conn, err := dial(dctx, "tcp", addr)
	if err != nil {
		d.add(statusWarn, "mail", "cannot reach the mail server %s: %v", addr, err)
		return
	}
	conn.Close()
	d.add(statusOK, "mail", "%s reachable (%s)", addr, m.Security)
}

func (d *doctor) checkCertificate() {
	tlsCfg := d.cfg.TLS
	switch {
	case tlsCfg.CertFile != "":
		cert, err := tls.LoadX509KeyPair(tlsCfg.CertFile, tlsCfg.KeyFile)
		if err != nil {
			d.add(statusFail, "certificate", "%v", err)
			return
		}
		leaf := cert.Leaf
		if leaf == nil {
			if leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
				d.add(statusFail, "certificate", "%v", err)
				return
			}
		}
		d.certValidity(leaf, certWarn, "renew it, put the files in place and send the server SIGHUP")
		if err := leaf.VerifyHostname(d.cfg.BaseURL.Hostname()); err != nil {
			d.add(statusWarn, "certificate", "%s does not cover %s, the host of base_url", tlsCfg.CertFile, d.cfg.BaseURL.Hostname())
		}
	case len(tlsCfg.ACMEDomains) > 0:
		for _, domain := range tlsCfg.ACMEDomains {
			leaf, err := cachedCert(filepath.Join(d.cfg.DataDir, acmeCacheDir, domain))
			switch {
			case errors.Is(err, fs.ErrNotExist):
				d.add(statusWarn, "certificate", "%s: not issued yet; it is requested with the first HTTPS connection for it", domain)
			case err != nil:
				d.add(statusFail, "certificate", "%s: the cached certificate does not read: %v", domain, err)
			default:
				d.certValidity(leaf, acmeWarn, "automatic renewal is overdue: look for ACME errors in the server's log")
			}
		}
	default:
		d.add(statusSkip, "certificate", "HTTPS is not served by this server (reverse proxy)")
	}
}

// certValidity adds the item for a certificate's dates.
func (d *doctor) certValidity(leaf *x509.Certificate, warn time.Duration, advice string) {
	left := leaf.NotAfter.Sub(d.now)
	names := strings.Join(leaf.DNSNames, ",")
	switch {
	case d.now.Before(leaf.NotBefore):
		d.add(statusFail, "certificate", "%s: not valid before %s", names, leaf.NotBefore.UTC().Format(time.RFC3339))
	case left <= 0:
		d.add(statusFail, "certificate", "%s: expired %s; %s", names, leaf.NotAfter.UTC().Format(time.RFC3339), advice)
	case left < warn:
		d.add(statusWarn, "certificate", "%s: expires in %d days (%s); %s", names, int(left.Hours()/24), leaf.NotAfter.UTC().Format(time.RFC3339), advice)
	default:
		d.add(statusOK, "certificate", "%s: valid for %d more days", names, int(left.Hours()/24))
	}
}

// cachedCert reads the leaf of a certificate autocert cached: the private key and the chain in
// PEM, in one file named after the domain.
func cachedCert(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return nil, errors.New("no certificate in it")
		}
		if b.Type == "CERTIFICATE" {
			return x509.ParseCertificate(b.Bytes)
		}
	}
}

func (d *doctor) checkDisk() {
	dir := d.cfg.DataDir
	// Before the first start, the disk the directory will be on.
	for {
		if _, err := os.Stat(dir); err == nil || filepath.Dir(dir) == dir {
			break
		}
		dir = filepath.Dir(dir)
	}
	free, err := freeSpace(dir)
	switch {
	case err != nil:
		d.add(statusWarn, "disk space", "cannot tell for %s: %v", dir, err)
	case free < diskFail:
		d.add(statusFail, "disk space", "%s free on the disk of %s: SQLite stops writing when it runs out", humanBytes(int64(free)), dir)
	case free < diskWarn:
		d.add(statusWarn, "disk space", "only %s free on the disk of %s", humanBytes(int64(free)), dir)
	default:
		d.add(statusOK, "disk space", "%s free on the disk of %s", humanBytes(int64(free)), dir)
	}
}

func (d *doctor) checkGit(ctx context.Context) {
	path, err := exec.LookPath(d.cfg.Git)
	if err != nil {
		d.add(statusWarn, "git", "%q not found: needed to fetch resource repositories (not for resources registered on the web)", d.cfg.Git)
		return
	}
	gctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(gctx, path, "--version").Output()
	if err != nil {
		d.add(statusWarn, "git", "%s does not run: %v", path, err)
		return
	}
	d.add(statusOK, "git", "%s (%s)", strings.TrimSpace(string(out)), path)
}

func (d *doctor) checkBackups() {
	if d.cfg.DatabaseScheme() != config.SchemeSQLite {
		d.add(statusSkip, "backups", "PostgreSQL: back it up with pg_dump or your provider's snapshots, and the master key apart")
		return
	}
	if d.cfg.BackupDir == "" {
		d.add(statusWarn, "backups", "no daily backup (backup.dir is not set): set it, or run %s backup regularly", brand.Server)
		return
	}
	entries, err := os.ReadDir(d.cfg.BackupDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		d.add(statusFail, "backups", "%v", err)
		return
	}
	var names []string
	for _, e := range entries {
		if dailyBackupName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		d.add(statusWarn, "backups", "none in %s yet; the server makes one a day", d.cfg.BackupDir)
		return
	}
	newest := slices.Max(names)
	stamp := strings.TrimSuffix(strings.TrimPrefix(newest, brand.CLILong+"-"), ".db")
	at, err := time.Parse(backupStamp, stamp)
	if err == nil && d.now.Sub(at) > backupStale {
		d.add(statusWarn, "backups", "the newest, %s, is %d hours old: the daily backup is not running", newest, int(d.now.Sub(at).Hours()))
		return
	}
	d.add(statusOK, "backups", "%d in %s, the newest %s", len(names), d.cfg.BackupDir, newest)
}
