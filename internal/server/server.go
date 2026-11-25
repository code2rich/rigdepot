// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/buildinfo"
	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/config"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/migrations"
)

// Exit codes of the server program.
const (
	exitOK       = 0
	exitFailure  = 1 // something went wrong while running, or the command is not implemented yet
	exitUsage    = 2 // wrong arguments or a configuration that does not load
	exitShutdown = 0 // a graceful shutdown on SIGINT or SIGTERM is a normal end
)

// Main runs the server with the given arguments (without the program name)
// and returns its exit code. SIGINT and SIGTERM shut it down gracefully.
func Main(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, args, &env{stdout: os.Stdout, stderr: os.Stderr, lookupEnv: os.LookupEnv})
}

// env is what a command gets from the process. Tests make their own.
type env struct {
	stdout, stderr io.Writer
	lookupEnv      func(string) (string, bool)
	// listening, when set, receives the address serve listens on once it
	// does. Tests use it to find a server started on port 0.
	listening chan<- string
	// mounts are the handlers serve mounts; see apiHandler.
	mounts func(*App) Mounts
}

type command struct {
	name    string // one or two words, such as "keys rotate"
	summary string
	run     func(ctx context.Context, e *env, args []string) int
}

// commands is the subcommand table of docs/architecture/08-deployment.md#管理子命令.
// The ones that run "notYet" are task M1-42 (and setup --from, which comes
// after the setup wizard); they are listed so that the command line is
// complete and says so instead of failing as unknown.
func commands() []command {
	return []command{
		{"serve", "start the server (the default)", runServe},
		{"migrate", "only run the database migrations", runMigrate},
		{"healthcheck", "check that the server on this machine answers, for container health checks", runHealthcheck},
		{"version", "print the version", runVersion},
		{"backup", "make a consistent snapshot of the database", notYet},
		{"restore", "check a snapshot (--check)", notYet},
		{"reset-admin", "reset the password of a local administrator", notYet},
		{"keys rotate", "re-wrap every data key with a new master key", notYet},
		{"audit verify", "verify the audit log's hash chain", notYet},
		{"setup", "complete the first setup from a file (--from)", notYet},
		{"config check", "print the effective configuration and where each value comes from", notYet},
		{"doctor", "check the environment", notYet},
	}
}

func run(ctx context.Context, args []string, e *env) int {
	if len(args) == 0 {
		return runServe(ctx, e, nil)
	}
	switch args[0] {
	case "--version", "-version":
		return runVersion(ctx, e, args[1:])
	case "help", "-h", "-help", "--help":
		usage(e.stdout)
		return exitOK
	}
	for _, c := range commands() {
		words := strings.Fields(c.name)
		if len(args) >= len(words) && strings.Join(args[:len(words)], " ") == c.name {
			return c.run(ctx, e, args[len(words):])
		}
	}
	if strings.HasPrefix(args[0], "-") {
		// Flags without a command are flags of serve.
		return runServe(ctx, e, args)
	}
	fmt.Fprintf(e.stderr, "%s: unknown command %q\n\n", brand.Server, strings.Join(args, " "))
	usage(e.stderr)
	return exitUsage
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "Usage: %s [command] [flags]\n\nCommands:\n", brand.Server)
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-14s %s\n", c.name, c.summary)
	}
	fmt.Fprintf(w, "\nRun %s <command> -h for the flags of a command. Every flag can also be set\nwith an environment variable or in the file named by %s.\n", brand.Server, config.ConfigEnv)
}

func notYet(_ context.Context, e *env, _ []string) int {
	fmt.Fprintf(e.stderr, "%s: this command is not implemented in this version yet\n", brand.Server)
	return exitFailure
}

func runVersion(_ context.Context, e *env, args []string) int {
	if len(args) > 0 {
		fmt.Fprintf(e.stderr, "%s version: takes no arguments\n", brand.Server)
		return exitUsage
	}
	fmt.Fprintln(e.stdout, buildinfo.Get().String(brand.Server))
	return exitOK
}

// loadConfig parses the flags of a command (every non-secret setting has
// one, plus --config) and loads the configuration.
func loadConfig(name string, e *env, args []string, extra func(*flag.FlagSet)) (*config.Config, int) {
	fs := flag.NewFlagSet(brand.Server+" "+name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	configFile := fs.String("config", "", "configuration file (TOML); also "+config.ConfigEnv)
	type flagValue struct {
		key   string
		value *string
	}
	values := map[string]flagValue{} // by flag name
	for _, s := range config.Settings() {
		if s.Flag != "" {
			values[s.Flag] = flagValue{s.Key, fs.String(s.Flag, "", s.Help+"; also "+s.Env)}
		}
	}
	if extra != nil {
		extra(fs)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, exitOK
		}
		return nil, exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(e.stderr, "%s %s: unexpected argument %q\n", brand.Server, name, fs.Arg(0))
		return nil, exitUsage
	}
	flags := map[string]string{}
	fs.Visit(func(f *flag.Flag) {
		if v, ok := values[f.Name]; ok {
			flags[v.key] = *v.value
		}
	})
	cfg, err := config.Load(config.Options{LookupEnv: e.lookupEnv, ConfigFile: *configFile, Flags: flags})
	if err != nil {
		fmt.Fprintf(e.stderr, "%s: the configuration is not valid:\n  %s\n", brand.Server,
			strings.ReplaceAll(logging.Redact(err.Error()), "\n", "\n  "))
		return nil, exitUsage
	}
	return cfg, exitOK
}

func newLogger(cfg *config.Config, w io.Writer) *slog.Logger {
	level, _ := logging.ParseLevel(cfg.LogLevel)
	format, _ := logging.ParseFormat(cfg.LogFormat)
	return logging.New(w, level, format)
}

// openDatabase creates the data directory when needed and opens the
// database.
func openDatabase(ctx context.Context, cfg *config.Config) (*dbx.DB, error) {
	// The data directory holds the master key and the repository cache even
	// with PostgreSQL. 0700: it belongs to the server's user alone.
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create the data directory: %w", err)
	}
	return dbx.Open(ctx, cfg.DatabaseURL)
}

func runServe(ctx context.Context, e *env, args []string) int {
	cfg, code := loadConfig("serve", e, args, nil)
	if cfg == nil {
		return code
	}
	log := newLogger(cfg, e.stdout)
	log.Info("starting", "version", buildinfo.Get().Version, "config_file", cfg.File, "database", cfg.DatabaseScheme())
	if err := serve(ctx, cfg, log, e); err != nil {
		log.Error("stopped", "err", err)
		return exitFailure
	}
	log.Info("stopped")
	return exitShutdown
}

func serve(ctx context.Context, cfg *config.Config, log *slog.Logger, e *env) error {
	warnNotYet(cfg, log)
	db, err := openDatabase(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	if cfg.AutoMigrate {
		applied, err := migrations.Apply(ctx, db)
		if err != nil {
			return err
		}
		for _, m := range applied {
			log.Info("migration applied", "name", m.Name)
		}
	} else {
		pending, err := migrations.Pending(ctx, db)
		if err != nil {
			return err
		}
		if len(pending) > 0 {
			return fmt.Errorf("%d database migrations have not run and %s is false; back up the database, then run %s migrate",
				len(pending), config.EnvName("AUTO_MIGRATE"), brand.Server)
		}
	}

	app := NewApp(cfg, log, db, Mounts{})
	mounts := apiMounts
	if e.mounts != nil {
		mounts = e.mounts
	}
	app.mounts = mounts(app)

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if e.listening != nil {
		e.listening <- ln.Addr().String()
	}
	return app.Serve(ctx, ln)
}

// warnNotYet logs the settings this version reads but does not act on yet,
// so that nobody thinks they are in effect.
func warnNotYet(cfg *config.Config, log *slog.Logger) {
	if cfg.MetricsAddr != "" {
		log.Warn("metrics are not implemented in this version yet; metrics_addr is ignored")
	}
	if cfg.BackupDir != "" {
		log.Warn("the built-in daily backup is not implemented in this version yet; backup.dir is ignored")
	}
	if len(cfg.ModulesDisabled) > 0 {
		log.Info("modules disabled by configuration", "modules", strings.Join(cfg.ModulesDisabled, ","))
	}
}

func runMigrate(ctx context.Context, e *env, args []string) int {
	cfg, code := loadConfig("migrate", e, args, nil)
	if cfg == nil {
		return code
	}
	log := newLogger(cfg, e.stdout)
	db, err := openDatabase(ctx, cfg)
	if err != nil {
		log.Error("migrate", "err", err)
		return exitFailure
	}
	defer db.Close()
	applied, err := migrations.Apply(ctx, db)
	for _, m := range applied {
		log.Info("migration applied", "name", m.Name)
	}
	if err != nil {
		log.Error("migrate", "err", err)
		return exitFailure
	}
	v, _ := db.SchemaVersion(ctx, migrations.Namespace)
	log.Info("database is up to date", "applied", len(applied), "version", v)
	return exitOK
}
