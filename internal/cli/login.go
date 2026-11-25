// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/device"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/keyring"
	"github.com/code2rich/rigdepot/internal/localconfig"
)

func init() { register("", newLoginCmd) }

// newLoginCmd: rgd login [server]. It signs this computer in with the device code flow: it prints
// a code and the page to approve it on, and waits until the person approves in a browser where
// they are signed in. It never opens a browser by itself; in a terminal it offers to on Enter.
// The device token and key go to the keychain; the server's address goes to config.toml.
func newLoginCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "login [server]",
		Short: a.p.T(msg.LoginShort),
		Long:  a.p.T(msg.LoginLong),
		Args:  a.maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.login(cmd.Context(), args)
		},
	}
}

// loginDoc is the JSON output of rgd login (schema rgd.login/v1).
type loginDoc struct {
	Server     string         `json:"server"`
	User       loginUserDoc   `json:"user"`
	Device     loginDeviceDoc `json:"device"`
	KeyStorage string         `json:"keyStorage"` // keyring or file
}

type loginUserDoc struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName,omitempty"`
}

type loginDeviceDoc struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// identity opens the device identity in the local data directory's keychain. A value that goes
// to a file because there is no system keychain is told once, when it happens.
func (a *app) identity() (*device.Identity, home.Dir, error) {
	if a.home == "" {
		return nil, home.Dir{}, a.noHomeError()
	}
	path := a.home
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, home.Dir{}, a.noHomeError()
		}
		path = abs
	}
	data, err := home.At(path)
	if err != nil {
		return nil, home.Dir{}, a.noHomeError()
	}
	store := keyring.Open(data, keyring.Options{OnFallback: func(f keyring.Fallback) {
		a.out.Warn(a.p.T(msg.LoginKeyringFallback, i18n.V("dir", a.showPath(f.Dir))))
	}})
	return device.New(store), data, nil
}

// connect returns a client for server with token ("" for none).
func (a *app) connect(cfg *localconfig.Config, server, token string) (*apiclient.ClientWithResponses, error) {
	return apiclient.Connect(apiclient.Options{Server: server, Token: token, CAFile: cfg.String(localconfig.KeyCAFile)})
}

// computer is what the server is told about this computer.
func (a *app) computer() device.Computer {
	hostname := a.env.Hostname
	if hostname == nil {
		hostname = os.Hostname
	}
	name, err := hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		name = "unknown"
	}
	name = strings.TrimSpace(name)
	for utf8.RuneCountInString(name) > 64 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return device.Computer{Name: name, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: apiclient.Version()}
}

func (a *app) login(ctx context.Context, args []string) error {
	cfg, err := a.config()
	if err != nil {
		return err
	}
	raw := cfg.String(localconfig.KeyServer)
	if len(args) == 1 {
		raw = args[0]
	}
	if raw == "" {
		return a.usageError("no_server", a.p.T(msg.LoginNoServer), "login")
	}
	server, err := apiclient.NormalServer(raw)
	if err != nil {
		return a.usageError("bad_server", a.p.T(msg.LoginBadServer, i18n.V("value", raw)), "login")
	}
	id, _, err := a.identity()
	if err != nil {
		return err
	}
	client, err := a.connect(cfg, server, "")
	if err != nil {
		return a.serverError(server, err)
	}

	creds, err := id.Login(ctx, client, device.LoginOptions{
		Server:   server,
		Computer: a.computer(),
		Prompt:   func(p device.Prompt) { a.loginPrompt(cfg, p) },
		Sleep:    a.env.sleep,
	})
	var already *device.AlreadyLoggedInError
	switch {
	case errors.As(err, &already):
		e := cliout.Fail(cliout.ExitFailure, "already_logged_in", a.p.T(msg.LoginAlready,
			i18n.V("address", already.Credentials.Server), i18n.V("user", already.Credentials.Username)))
		e.Hint = a.p.T(msg.LoginAlreadyHint)
		return e
	case errors.Is(err, device.ErrDenied):
		return cliout.Fail(cliout.ExitAuth, "login_denied", a.p.T(msg.LoginDenied))
	case errors.Is(err, device.ErrExpired):
		return cliout.Fail(cliout.ExitAuth, "login_expired", a.p.T(msg.LoginExpired))
	case err != nil:
		return a.deviceError(server, err)
	}

	// The server goes into config.toml, so that the next commands use it.
	if cfg.String(localconfig.KeyServer) != server || cfg.Get(localconfig.KeyServer).Source != localconfig.SourceFile {
		if _, err := localconfig.SaveFile(cfg.Path(), localconfig.KeyServer, server); err != nil {
			a.out.Warn(a.p.T(msg.LoginConfigFailed, i18n.V("path", a.showPath(cfg.Path())), i18n.V("detail", err.Error())))
		}
	}
	doc := loginDoc{
		Server:     creds.Server,
		User:       loginUserDoc{ID: creds.UserID, Username: creds.Username, DisplayName: creds.DisplayName},
		Device:     loginDeviceDoc{ID: creds.DeviceID, Name: a.computer().Name},
		KeyStorage: id.KeyStorage(),
	}
	return a.out.Result(cliout.Schema("login", 1), doc, func(w io.Writer) error {
		user := creds.Username
		if creds.DisplayName != "" && creds.DisplayName != creds.Username {
			user = creds.DisplayName + " (" + creds.Username + ")"
		}
		_, err := fmt.Fprintln(w, a.p.T(msg.LoginDone, i18n.V("address", creds.Server), i18n.V("user", user), i18n.V("device", doc.Device.Name)))
		return err
	})
}

// loginPrompt shows the code and the page to approve it on. It goes to standard error, also with
// --json: the person needs it either way. In a terminal, Enter opens the page in the browser.
func (a *app) loginPrompt(cfg *localconfig.Config, p device.Prompt) {
	url := p.URL
	if url == "" {
		url = p.Page
	}
	w := a.out.Stderr()
	fmt.Fprintln(w, a.p.T(msg.LoginPrompt, i18n.V("url", url), i18n.V("code", p.UserCode)))
	if a.interactive(cfg) && a.env.OpenBrowser != nil {
		fmt.Fprintln(w, a.p.T(msg.LoginPromptEnter))
		go func() {
			if _, err := bufio.NewReader(a.env.Stdin).ReadString('\n'); err == nil {
				_ = a.env.OpenBrowser(url)
			}
		}()
	}
	fmt.Fprintln(w, a.p.T(msg.LoginWaiting, i18n.Count(int(p.ExpiresIn.Minutes()))))
}

// deviceError explains a failure of signing in or out that is not the person's answer.
func (a *app) deviceError(server string, err error) error {
	var se *device.StoreError
	if errors.As(err, &se) {
		e := cliout.Fail(cliout.ExitStateUnknown, "keyring_unknown", a.p.T(msg.LoginKeyringUnknown, i18n.V("detail", se.Err.Error())))
		e.Hint = a.p.T(msg.ErrorRunDoctor)
		e.Err = err
		return e
	}
	return a.serverError(server, err)
}

// serverError explains an answer of the server or the network.
func (a *app) serverError(server string, err error) error {
	if apiclient.Unreachable(err) {
		e := cliout.Fail(cliout.ExitUnreachable, "server_unreachable", a.p.T(msg.LoginUnreachable, i18n.V("address", server), i18n.V("detail", err.Error())))
		e.Err = err
		return e
	}
	var ae *apiclient.Error
	if errors.As(err, &ae) && ae.Status == 401 {
		e := cliout.Fail(cliout.ExitAuth, "not_logged_in", a.p.T(msg.LoginServerFailed, i18n.V("address", server), i18n.V("detail", err.Error())))
		e.Err = err
		return e
	}
	e := cliout.Fail(cliout.ExitFailure, "server_failed", a.p.T(msg.LoginServerFailed, i18n.V("address", server), i18n.V("detail", err.Error())))
	e.Err = err
	return e
}
