// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	stdsync "sync"

	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/executor"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/localconfig"
	"github.com/code2rich/rigdepot/internal/repoformat"
	"github.com/code2rich/rigdepot/internal/sync"
	"github.com/code2rich/rigdepot/locales"
)

// Env is everything the program takes from the process, and the few parts of
// the program that tests replace. Main uses the real one; tests pass their
// own, so that nothing depends on the machine running them (its terminal, its
// language, its home directory, the tools installed on it).
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	// HomeDir returns the person's home directory.
	HomeDir func() (string, error)
	// SystemLanguages returns the operating system's preferred languages.
	SystemLanguages func() []string
	// IsTerminal reports whether a stream is an interactive terminal.
	IsTerminal func(any) bool
	// GOOS decides how paths are shown (~/… or %USERPROFILE%\…).
	GOOS string

	// Adapters are the AI tools sync, status and uninstall work with; nil means every
	// registered adapter (builtin/all).
	Adapters []adapter.Adapter
	// Kinds are the resource kinds a repository is read with; nil means the registered ones.
	Kinds repoformat.Kinds
	// NewExecutor makes the executors of the sync engine; nil means the real one.
	NewExecutor func(executor.Roots, executor.State) (sync.Executor, error)
	// Knowledge fetches the knowledge bases a sync delivers; nil means the person's own git.
	Knowledge sync.Fetcher
}

// Main runs the command-line program with the given arguments (without the
// program name) and returns its exit code.
func Main(args []string) int {
	return Run(args, Env{
		Stdin:           os.Stdin,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
		Getenv:          os.Getenv,
		HomeDir:         os.UserHomeDir,
		SystemLanguages: i18n.SystemLanguages,
		IsTerminal: func(f any) bool {
			file, ok := f.(*os.File)
			return ok && term.IsTerminal(int(file.Fd()))
		},
		GOOS: runtime.GOOS,
	})
}

// Run is Main with the process environment passed in.
func Run(args []string, env Env) int {
	a, err := newApp(args, env)
	if err != nil {
		return int(a.out.Fail(err))
	}
	root := newRoot(a)
	root.SetArgs(args)
	root.SetIn(env.Stdin)
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	_, err = root.ExecuteContextC(i18n.NewContext(context.Background(), a.p))
	return int(a.out.Fail(a.explain(err)))
}

// app is what every command gets: the output, the printer for the chosen
// language, the resolved local configuration and the process environment.
type app struct {
	env    Env
	out    *cliout.Output
	p      *i18n.Printer
	cfg    *localconfig.Config
	cfgErr error // why the configuration file or environment cannot be used
	home   string
	global globalFlags
}

// config returns the local configuration for a command that depends on it.
// When the file cannot be read or parsed, or an environment variable is
// invalid, the command fails with that: its content is unknown, not empty.
// Commands that work without configuration (version, completion, help) do
// not call it.
func (a *app) config() (*localconfig.Config, error) {
	if a.cfgErr == nil {
		return a.cfg, nil
	}
	var fe *localconfig.FileError
	if errors.As(a.cfgErr, &fe) {
		e := cliout.Fail(cliout.ExitFailure, "config_unreadable",
			a.p.T(msg.ErrorConfigUnreadable, i18n.V("path", a.showPath(fe.Path)), i18n.V("detail", fe.Err.Error())))
		e.Err = fe
		return nil, e
	}
	var ee *localconfig.EnvError
	if errors.As(a.cfgErr, &ee) {
		return nil, cliout.Usage("env_invalid", a.p.T(msg.ErrorEnvInvalid,
			i18n.V("name", ee.Name), i18n.V("value", ee.Err.Value), i18n.V("reason", a.reason(ee.Err))))
	}
	if errors.Is(a.cfgErr, errNoHome) {
		return nil, a.noHomeError()
	}
	return nil, a.cfgErr
}

// errNoHome: neither --home nor RGD_HOME is set and the home directory is
// unknown, so there is no place for the local data.
var errNoHome = errors.New("the home directory is unknown")

func (a *app) noHomeError() error {
	return cliout.Fail(cliout.ExitFailure, "no_home", a.p.T(msg.ErrorNoHome, i18n.V("env", brand.EnvPrefix+"HOME")))
}

// showPath shortens a path below the home directory for people.
func (a *app) showPath(p string) string {
	home, err := a.env.HomeDir()
	if err != nil {
		return p
	}
	return cliout.ShowPath(p, home, a.env.GOOS)
}

var (
	catalogOnce stdsync.Once
	catalog     *i18n.Catalog
	catalogErr  error
)

// loadCatalog reads the embedded messages the command line uses.
func loadCatalog() (*i18n.Catalog, error) {
	catalogOnce.Do(func() {
		catalog, catalogErr = i18n.Load(locales.FS, "catalog", "check", "cli")
	})
	return catalog, catalogErr
}

// newApp resolves, before the command tree is built, what the tree itself
// depends on: the language its help is written in and how output is
// rendered. The global options are read here by a first, lenient pass over
// the arguments; cobra parses them again later and reports what is wrong.
// On error the returned app still has an output to report it with.
func newApp(args []string, env Env) (*app, error) {
	a := &app{env: env, global: prescan(args)}
	a.out = cliout.New(env.Stdout, env.Stderr, cliout.Options{JSON: a.global.json})
	cat, err := loadCatalog()
	if err != nil {
		return a, err
	}
	a.p = cat.Printer(cat.Match())

	// The configuration file lives in the local data directory, so that
	// directory cannot come from the file: --home, then RGD_HOME, then the
	// default.
	switch {
	case a.global.home != "":
		a.home = a.global.home
	case env.Getenv(brand.EnvPrefix+"HOME") != "":
		a.home = env.Getenv(brand.EnvPrefix + "HOME")
	default:
		if h, err := env.HomeDir(); err == nil && h != "" {
			a.home = filepath.Join(h, brand.Home)
		}
	}
	cfgPath := ""
	if a.home != "" {
		cfgPath = filepath.Join(a.home, localconfig.FileName)
	}
	a.cfg, a.cfgErr = localconfig.Load(cfgPath, env.Getenv)
	if a.home == "" {
		a.cfgErr = errNoHome
	}

	// Global options go on top of the environment and the file. Only
	// --lang can carry a value the setting rejects.
	badLang := false
	for _, f := range []struct {
		key, flag, value string
		set              bool
	}{
		{localconfig.KeyHome, "--home", a.global.home, a.global.home != ""},
		{localconfig.KeyLang, "--lang", a.global.lang, a.global.langSet},
		{localconfig.KeyNoColor, "--no-color", "true", a.global.noColor},
		{localconfig.KeyNonInteractive, "--non-interactive", "true", a.global.nonInteractive},
		{localconfig.KeyLogLevel, "-v", localconfig.LogDebug, a.global.verbose},
		{localconfig.KeyLogLevel, "-q", localconfig.LogError, a.global.quiet},
	} {
		if f.set && a.cfg.SetFlag(f.key, f.flag, f.value) != nil {
			badLang = true
		}
	}

	if a.home != "" {
		_ = a.cfg.SetDefault(localconfig.KeyHome, a.home) // a path is always accepted
	}

	// Language: --lang, RGD_LANG, the file's lang, the system, English.
	var prefs []string
	if v := a.cfg.Get(localconfig.KeyLang); v.IsSet() {
		prefs = append(prefs, v.Str)
	}
	prefs = append(prefs, env.SystemLanguages()...)
	a.p = cat.Printer(cat.Match(prefs...))

	level := a.cfg.String(localconfig.KeyLogLevel)
	verbosity := cliout.Normal
	switch level {
	case localconfig.LogDebug:
		verbosity = cliout.Verbose
	case localconfig.LogWarn, localconfig.LogError:
		verbosity = cliout.Quiet
	}
	a.out = cliout.New(env.Stdout, env.Stderr, cliout.Options{
		JSON:      a.global.json,
		Color:     cliout.ColorEnabled(a.cfg.Bool(localconfig.KeyNoColor), a.global.json, env.IsTerminal(env.Stdout)),
		Verbosity: verbosity,
	})

	if a.global.verbose && a.global.quiet {
		return a, a.usageError("verbose_and_quiet", a.p.T(msg.ErrorVerboseAndQuiet), "")
	}
	if badLang {
		return a, a.usageError("bad_lang", a.p.T(msg.ErrorBadLang, i18n.V("value", a.global.lang)), "")
	}
	a.reportWarnings()
	return a, nil
}

// reportWarnings tells the person about configuration that was ignored. A
// key this version does not know is only mentioned with -v: a newer version
// may have written it.
func (a *app) reportWarnings() {
	for _, w := range a.cfg.Warnings() {
		origin := w.Origin
		if origin == a.cfg.Path() {
			origin = a.showPath(origin)
		}
		switch w.Kind {
		case localconfig.WarnUnknownKey:
			a.out.Debug(a.p.T(msg.ConfigWarnUnknownKey, i18n.V("key", w.Key), i18n.V("origin", origin)))
		case localconfig.WarnFlagOnly:
			a.out.Warn(a.p.T(msg.ConfigWarnFlagOnly, i18n.V("key", w.Key), i18n.V("origin", origin), i18n.V("flag", flagName(w.Key))))
		case localconfig.WarnNotInFile:
			a.out.Warn(a.p.T(msg.ConfigWarnNotInFile, i18n.V("key", w.Key), i18n.V("origin", origin)))
		}
	}
}

// flagName returns the option that sets a setting, such as --yes-global.
func flagName(key string) string {
	s, _ := localconfig.Lookup(key)
	return s.Flag
}

// globalFlags are the options every command takes.
type globalFlags struct {
	json           bool
	lang           string
	langSet        bool
	home           string
	noColor        bool
	nonInteractive bool
	verbose        bool
	quiet          bool
}

// defineGlobalFlags declares the global options on fs. p is nil for the
// first pass, which needs no help text.
func defineGlobalFlags(fs *pflag.FlagSet, g *globalFlags, p *i18n.Printer) {
	t := func(key string) string {
		if p == nil {
			return ""
		}
		return p.T(key)
	}
	fs.BoolVar(&g.json, "json", false, t(msg.FlagJSON))
	fs.StringVar(&g.lang, "lang", "", t(msg.FlagLang))
	fs.StringVar(&g.home, "home", "", t(msg.FlagHome))
	fs.BoolVar(&g.noColor, "no-color", false, t(msg.FlagNoColor))
	fs.BoolVar(&g.nonInteractive, "non-interactive", false, t(msg.FlagNonInteractive))
	fs.BoolVarP(&g.verbose, "verbose", "v", false, t(msg.FlagVerbose))
	fs.BoolVarP(&g.quiet, "quiet", "q", false, t(msg.FlagQuiet))
}

// prescan reads the global options from the arguments without knowing the
// command tree. Unknown options are skipped and nothing is reported: cobra
// parses everything again with the tree and reports what is wrong.
func prescan(args []string) globalFlags {
	var g globalFlags
	fs := pflag.NewFlagSet("prescan", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.ParseErrorsAllowlist.UnknownFlags = true
	defineGlobalFlags(fs, &g, nil)
	fs.BoolP("help", "h", false, "")
	_ = fs.Parse(args)
	g.langSet = fs.Changed("lang")
	return g
}
