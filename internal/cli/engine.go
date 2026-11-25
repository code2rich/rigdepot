// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/buildinfo"
	"github.com/code2rich/rigdepot/internal/bundle"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/device"
	"github.com/code2rich/rigdepot/internal/gitrepo"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/localconfig"
	"github.com/code2rich/rigdepot/internal/manifest"
	"github.com/code2rich/rigdepot/internal/repoformat"
	"github.com/code2rich/rigdepot/internal/sync"
)

// This file is what sync, status, uninstall, enable and disable share: the engine for this
// computer, the desired state from the resource repository, and how their failures are told.

// local is the engine and what it works on.
type local struct {
	cfg      *localconfig.Config
	data     home.Dir
	userHome string
	eng      *sync.Engine
}

// local returns the sync engine for this computer. The configuration has to be readable: what it
// says (which tools are switched off, where the repository is) is not guessed.
func (a *app) local() (*local, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	if a.home == "" {
		return nil, a.noHomeError()
	}
	userHome, err := a.env.HomeDir()
	if err != nil || userHome == "" {
		return nil, a.noHomeError()
	}
	dataPath := a.home
	if !filepath.IsAbs(dataPath) {
		if dataPath, err = filepath.Abs(dataPath); err != nil {
			return nil, a.noHomeError()
		}
	}
	data, err := home.At(dataPath)
	if err != nil {
		return nil, a.noHomeError()
	}
	eng, err := sync.New(sync.Config{
		Adapters:    a.env.Adapters,
		Data:        data,
		System:      sync.System{GOOS: a.env.GOOS, Home: userHome, Getenv: a.env.Getenv},
		NewExecutor: a.env.NewExecutor,
	})
	if err != nil {
		return nil, err
	}
	return &local{cfg: cfg, data: data, userHome: userHome, eng: eng}, nil
}

// adapters returns the tools this run knows, in identifier order.
func (a *app) adapters() []adapter.Adapter {
	if a.env.Adapters != nil {
		out := slices.Clone(a.env.Adapters)
		slices.SortFunc(out, func(x, y adapter.Adapter) int { return strings.Compare(x.ID(), y.ID()) })
		return out
	}
	return adapter.All()
}

// sourceFlags say where the resources come from: --repo and what goes with it.
type sourceFlags struct {
	repo, branch, dir string
	products          []string
}

func (a *app) addSourceFlags(cmd *cobra.Command, f *sourceFlags) {
	fs := cmd.Flags()
	fs.StringVar(&f.repo, "repo", "", a.p.T(msg.FlagRepo))
	fs.StringVar(&f.branch, "branch", "", a.p.T(msg.FlagBranch))
	fs.StringVar(&f.dir, "dir", "", a.p.T(msg.FlagDir))
	fs.StringArrayVar(&f.products, "product", nil, a.p.T(msg.FlagProduct))
}

// errNoSource: neither --repo nor repo.url says where the resources are.
var errNoSource = errors.New("no resource repository configured")

// source resolves the repository to read: the flags, then the configuration (repo.url, which
// RGD_REPO overrides; repo.branch; repo.products).
func (l *local) source(f sourceFlags) (gitrepo.Source, []string, error) {
	url := f.repo
	if url == "" {
		url = l.cfg.String(localconfig.KeyRepoURL)
	}
	if url == "" {
		return gitrepo.Source{}, nil, errNoSource
	}
	// A local directory is recorded and fetched by its absolute path: the manifest must still
	// name the same repository when rgd runs from another directory.
	if fi, err := os.Stat(url); err == nil && fi.IsDir() {
		if abs, err := filepath.Abs(url); err == nil {
			url = abs
		}
	}
	ref := f.branch
	if ref == "" {
		ref = l.cfg.String(localconfig.KeyRepoBranch)
	}
	products := f.products
	if len(products) == 0 {
		products = l.cfg.List(localconfig.KeyRepoProducts)
	}
	return gitrepo.Source{URL: url, Ref: ref, Dir: f.dir}, products, nil
}

// desiredState computes what should be on this computer, from the server or the repository
// (resolveSource).
func (a *app) desiredState(ctx context.Context, l *local, f sourceFlags) (*desired.State, error) {
	st, _, err := a.resolveSource(ctx, l, f)
	return st, err
}

// remote is the server a desired state came from: the sign-in, the client with the device token
// and what the server said.
type remote struct {
	creds  device.Credentials
	client *apiclient.ClientWithResponses
	state  *desired.ServerState
}

// apply puts what the server says into the options of a plan: a tool turned off on the server
// is not written to here either (the stricter side wins), and a state kept from earlier, while
// the server cannot be reached, removes nothing. A nil remote changes nothing.
func (r *remote) apply(opt *sync.Options) {
	if r == nil {
		return
	}
	for _, id := range r.state.Org.ToolsDisabled {
		if !slices.Contains(opt.Disabled, id) {
			opt.Disabled = append(opt.Disabled, id)
		}
	}
	opt.Keep = r.state.Cached
}

// resolveSource decides where the resources come from and computes the desired state:
//
//   - --repo: that repository;
//   - a server is configured (rgd login wrote it) and this computer is signed in to it: the
//     server;
//   - repo.url: that repository;
//   - otherwise errNoSource, or errNotSignedIn when a server is configured.
//
// From the server, remote is set.
func (a *app) resolveSource(ctx context.Context, l *local, f sourceFlags) (*desired.State, *remote, error) {
	if f.repo == "" {
		if server := l.cfg.String(localconfig.KeyServer); server != "" {
			r, err := a.fromServer(ctx, l, server)
			switch {
			case err == nil:
				return r.state.State, r, nil
			case !errors.Is(err, errNotSignedIn):
				return nil, nil, err
			case l.cfg.String(localconfig.KeyRepoURL) == "":
				return nil, nil, a.notSignedInError(server)
			}
			// Signed out, with a repository configured: that is where the resources come from.
		}
	}
	src, products, err := l.source(f)
	if err != nil {
		return nil, nil, err
	}
	sel, err := desired.ReadSelection(l.data)
	if err != nil {
		return nil, nil, a.stateError(err)
	}
	repo, ds, err := a.readRepo(ctx, l, src)
	if err != nil {
		return nil, nil, err
	}
	return desired.FromRepo(repo, ds, desired.Filter{Products: products, Selection: sel}), nil, nil
}

// errNotSignedIn: a server is configured but this computer is not signed in to it.
var errNotSignedIn = errors.New("not signed in to the configured server")

// signedIn returns the client for the server this computer is signed in to: errNotSignedIn
// when it is not signed in to server, a *cliout.Error when the keychain cannot be read.
func (a *app) signedIn(cfg *localconfig.Config, server string) (device.Credentials, *apiclient.ClientWithResponses, error) {
	id, _, err := a.identity()
	if err != nil {
		return device.Credentials{}, nil, err
	}
	creds, err := id.CredentialsFor(server)
	if errors.Is(err, device.ErrNotLoggedIn) {
		return creds, nil, errNotSignedIn
	}
	if err != nil {
		return creds, nil, a.deviceError(server, err)
	}
	client, err := a.connect(cfg, creds.Server, creds.Token)
	if err != nil {
		return creds, nil, a.serverError(creds.Server, err)
	}
	return creds, client, nil
}

// fromServer asks the server what this computer should have.
func (a *app) fromServer(ctx context.Context, l *local, server string) (*remote, error) {
	creds, client, err := a.signedIn(l.cfg, server)
	if err != nil {
		return nil, err
	}
	st, err := desired.FromServer(ctx, client, desired.ServerOptions{
		URL: creds.Server, Data: l.data, Kinds: a.env.Kinds, Version: apiclient.Version(),
	})
	if err != nil {
		return nil, a.remoteError(creds.Server, err)
	}
	if st.Cached {
		a.out.Warn(a.p.T(msg.RemoteCached, i18n.V("address", creds.Server), i18n.V("detail", st.Unreachable.Error()),
			i18n.V("time", st.FetchedAt.Local().Format("2006-01-02 15:04"))))
	}
	for _, f := range st.Failed {
		detail := ""
		if len(f.Issues) > 0 {
			detail = f.Issues[0].Message
		}
		a.out.Warn(a.p.T(msg.RemoteUnavailable, i18n.V("resource", f.Ref.Code), i18n.V("detail", detail)))
	}
	return &remote{creds: creds, client: client, state: st}, nil
}

// notSignedInError: a server is configured, the computer is not signed in, and there is no
// repository to fall back on.
func (a *app) notSignedInError(server string) error {
	e := cliout.Fail(cliout.ExitAuth, "not_logged_in", a.p.T(msg.RemoteNotLoggedIn, i18n.V("address", server)))
	e.Err = errNotSignedIn
	return e
}

// remoteError tells why the server's desired state could not be had. Nothing was changed.
func (a *app) remoteError(server string, err error) error {
	where, detail := i18n.V("address", server), i18n.V("detail", err.Error())
	var e *cliout.Error
	var old *desired.TooOldError
	var ae *apiclient.Error
	switch {
	case errors.Is(err, context.Canceled):
		return err
	case errors.As(err, &old):
		e = cliout.Fail(cliout.ExitIncompatible, "cli_too_old", a.p.T(msg.RemoteTooOld, where, i18n.V("min", old.Min), i18n.V("version", old.Have)))
	case errors.Is(err, desired.ErrSignedOut):
		e = cliout.Fail(cliout.ExitAuth, "signed_out", a.p.T(msg.RemoteSignedOut, where, detail))
		e.Hint = a.p.T(msg.RemoteSignedOutHint)
	case errors.Is(err, desired.ErrUnreachable):
		e = cliout.Fail(cliout.ExitUnreachable, "server_unreachable", a.p.T(msg.RemoteUnreachable, where, detail))
	case errors.As(err, &ae):
		e = cliout.Fail(cliout.ExitFailure, "server_failed", a.p.T(msg.RemoteFailed, where, detail))
	default:
		e = cliout.Fail(cliout.ExitFailure, "desired_unkept", a.p.T(msg.RemoteKeepFailed, where, detail))
	}
	e.Err = err
	return e
}

// readRepo fetches and reads the repository.
func (a *app) readRepo(ctx context.Context, l *local, src gitrepo.Source) (*repoformat.Repo, desired.Source, error) {
	fetcher, err := gitrepo.New(gitrepo.Options{
		Git:      l.cfg.String(localconfig.KeyGit),
		CacheDir: l.data.Join(home.CacheDir, "repos"),
		// The address is the person's own: typed after --repo or written into their own
		// configuration.
		AllowLocal: true,
	})
	if err != nil {
		return nil, desired.Source{}, a.sourceError(src.URL, err)
	}
	repo, ds, err := desired.ReadRepo(ctx, fetcher, src, repoformat.Options{Kinds: a.env.Kinds, Version: buildinfo.Get().Version})
	if err != nil {
		return nil, desired.Source{}, a.sourceError(src.URL, err)
	}
	return repo, ds, nil
}

// noSourceError says how to name the repository.
func (a *app) noSourceError(path string) *cliout.Error {
	return a.usageError("no_source", a.p.T(msg.SourceNone, i18n.V("key", localconfig.KeyRepoURL)), path)
}

// sourceError tells why the repository could not be had. Nothing was changed because of it.
func (a *app) sourceError(url string, err error) error {
	detail := i18n.V("detail", err.Error())
	where := i18n.V("url", url)
	var e *cliout.Error
	switch {
	case errors.Is(err, errNoSource):
		return a.noSourceError(brand.CLI)
	case errors.Is(err, gitrepo.ErrNoGit):
		e = cliout.Fail(cliout.ExitFailure, "git_missing", a.p.T(msg.SourceNoGit, detail, i18n.V("env", brand.EnvPrefix+"GIT")))
	case errors.Is(err, gitrepo.ErrAddress):
		e = cliout.Usage("repo_address", a.p.T(msg.SourceAddress, where, detail))
	case errors.Is(err, gitrepo.ErrFetch), errors.Is(err, gitrepo.ErrTimeout):
		e = cliout.Fail(cliout.ExitUnreachable, "repo_unreachable", a.p.T(msg.SourceUnreachable, where, detail))
	case errors.Is(err, gitrepo.ErrNoRef), errors.Is(err, gitrepo.ErrNoDir):
		e = cliout.Fail(cliout.ExitFailure, "repo_no_ref", a.p.T(msg.SourceNoRef, where, detail))
	case errors.Is(err, repoformat.ErrNewerFormat), errors.Is(err, repoformat.ErrNeedsNewer):
		e = cliout.Fail(cliout.ExitIncompatible, "repo_incompatible", a.p.T(msg.SourceIncompatible, where, detail))
	case errors.Is(err, repoformat.ErrNoConfig):
		e = cliout.Fail(cliout.ExitFailure, "not_a_repo", a.p.T(msg.SourceNotARepo, where, i18n.V("file", brand.RepoConfig)))
	case errors.Is(err, gitrepo.ErrTooLarge), errors.Is(err, bundle.ErrTreeTooLarge):
		e = cliout.Fail(cliout.ExitFailure, "repo_too_large", a.p.T(msg.SourceUnreadable, where, detail))
	default:
		if errors.Is(err, context.Canceled) {
			return err
		}
		e = cliout.Fail(cliout.ExitFailure, "repo_unreadable", a.p.T(msg.SourceUnreadable, where, detail))
	}
	e.Err = err
	return e
}

// stateError tells why the engine would not go on. Only the failures a person can act on get
// their own code; anything else is a defect and reported as internal.
func (a *app) stateError(err error) error {
	if err == nil {
		return nil
	}
	var e *cliout.Error
	if errors.As(err, &e) {
		return err
	}
	detail := i18n.V("detail", err.Error())
	switch {
	case errors.Is(err, sync.ErrUnknownTool):
		var ids []string
		for _, t := range a.adapters() {
			ids = append(ids, t.ID())
		}
		e = a.usageError("unknown_tool", a.p.T(msg.ErrorUnknownTool, detail, i18n.V("tools", strings.Join(ids, ", "))), "")
	case errors.Is(err, manifest.ErrLocked):
		e = cliout.Fail(cliout.ExitStateUnknown, "locked", a.p.T(msg.ErrorLocked))
	case errors.Is(err, manifest.ErrUnknown):
		e = cliout.Fail(cliout.ExitStateUnknown, "manifest_unreadable", a.p.T(msg.ErrorManifestUnreadable, detail))
		e.Hint = a.p.T(msg.ErrorRunDoctor)
	case errors.Is(err, desired.ErrSelectionUnknown):
		e = cliout.Fail(cliout.ExitStateUnknown, "selection_unreadable", a.p.T(msg.ErrorSelectionUnreadable, detail))
		e.Hint = a.p.T(msg.ErrorRunDoctor)
	case errors.Is(err, sync.ErrStalePlan):
		e = cliout.Fail(cliout.ExitStateUnknown, "stale_plan", a.p.T(msg.ErrorStalePlan))
	default:
		return err
	}
	e.Err = err
	return e
}

// showTarget shows a target path ("~/…" or absolute) the way people read paths on this system.
func (a *app) showTarget(userHome, p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = filepath.Join(userHome, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/")))
	}
	return cliout.ShowPath(p, userHome, a.env.GOOS)
}

// showData shows a path inside the local data directory.
func (a *app) showData(l *local, rel ...string) string {
	return cliout.ShowPath(l.data.Join(rel...), l.userHome, a.env.GOOS)
}

// interactive reports whether a person can be asked: standard input and standard error are a
// terminal and --non-interactive (RGD_NONINTERACTIVE) does not say otherwise.
func (a *app) interactive(cfg *localconfig.Config) bool {
	return !cfg.Bool(localconfig.KeyNonInteractive) && a.env.IsTerminal(a.env.Stdin) && a.env.IsTerminal(a.env.Stderr)
}
