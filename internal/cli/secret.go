// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/device"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/localconfig"
	"github.com/code2rich/rigdepot/internal/localsecrets"
)

func init() {
	register("", newSecretCmd)
	register("secret", newSecretSetCmd)
	register("secret", newSecretListCmd)
	register("secret", newSecretRmCmd)
	register("secret", newSecretPullCmd)
}

// newSecretCmd: rgd secret, the person's own secrets on the server and their sealed copies on
// this computer.
func newSecretCmd(a *app) *cobra.Command {
	return a.group("secret", a.p.T(msg.SecretShort))
}

// signedIn is what the secret commands work with: the device identity, its sign-in, a client
// with its token, and the vault.
type signedIn struct {
	id     *device.Identity
	creds  device.Credentials
	client *apiclient.ClientWithResponses
	vault  *localsecrets.Vault
}

func (s *signedIn) owner() localsecrets.Owner {
	return localsecrets.Owner{Server: s.creds.Server, UserID: s.creds.UserID, DeviceID: s.creds.DeviceID}
}

// signIn opens the sign-in of this computer, for a command that needs the server.
func (a *app) signIn() (*signedIn, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	id, data, err := a.identity()
	if err != nil {
		return nil, err
	}
	client, creds, err := id.Connect(apiclient.Options{CAFile: cfg.String(localconfig.KeyCAFile)})
	if err != nil {
		return nil, a.signInError(creds.Server, err)
	}
	return &signedIn{id: id, creds: creds, client: client, vault: localsecrets.New(data)}, nil
}

// signInError explains why a command that needs the server cannot use this computer's sign-in.
func (a *app) signInError(server string, err error) error {
	if errors.Is(err, device.ErrNotLoggedIn) {
		e := cliout.Fail(cliout.ExitAuth, "not_logged_in", a.p.T(msg.SecretNotSignedIn))
		e.Hint = a.p.T(msg.SecretNotSignedInHint)
		return e
	}
	return a.deviceError(server, err)
}

// vaultError explains a vault that cannot be read.
func (a *app) vaultError(v *localsecrets.Vault, err error) error {
	if errors.Is(err, localsecrets.ErrUnknown) {
		e := cliout.Fail(cliout.ExitStateUnknown, "secrets_unreadable", a.p.T(msg.SecretVaultUnreadable,
			i18n.V("path", a.showPath(v.Path())), i18n.V("detail", err.Error())))
		e.Hint = a.p.T(msg.ErrorRunDoctor)
		e.Err = err
		return e
	}
	return err
}

// secretNameArg checks the name argument.
func (a *app) secretNameArg(cmd *cobra.Command, name string) error {
	if !resource.ValidSecretName(name) {
		return a.usageError("bad_secret_name", a.p.T(msg.SecretBadName, i18n.V("name", name)), cmd.CommandPath())
	}
	return nil
}

// secretDoc is one secret in the JSON output (schema rgd.secret/v1 and rgd.secrets/v1). It never
// holds a value.
type secretDoc struct {
	Name       string     `json:"name"`
	Label      string     `json:"label,omitempty"`
	Howto      string     `json:"howto,omitempty"`
	UsedBy     []string   `json:"usedBy"`
	Configured bool       `json:"configured"`
	Local      bool       `json:"local"` // this computer has a sealed copy
	Version    int        `json:"version,omitempty"`
	UpdatedAt  *time.Time `json:"updatedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
}

func newSecretSetCmd(a *app) *cobra.Command {
	var expires string
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: a.p.T(msg.SecretSetShort),
		Long:  a.p.T(msg.SecretSetLong),
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := a.secretNameArg(cmd, name); err != nil {
				return err
			}
			var exp *time.Time
			if expires != "" {
				t, err := parseDate(expires)
				if err != nil {
					return a.usageError("bad_expires", a.p.T(msg.SecretBadExpires, i18n.V("value", expires)), cmd.CommandPath())
				}
				exp = &t
			}
			s, err := a.signIn()
			if err != nil {
				return err
			}
			value, err := a.readSecret(name)
			if err != nil {
				return err
			}
			defer clear(value)
			return a.secretSet(cmd.Context(), s, name, value, exp)
		},
	}
	cmd.Flags().StringVar(&expires, "expires", "", a.p.T(msg.SecretFlagExpires))
	return cmd
}

// parseDate reads 2027-05-01 or an RFC 3339 time.
func parseDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t.UTC(), nil
	}
	return time.Parse(time.RFC3339, s)
}

// readSecret reads the value: in a terminal without showing it, otherwise one line from
// standard input (printf '%s' "$TOKEN" | rgd secret set NAME). It never comes from an argument,
// which would leave it in the shell's history and the process list.
func (a *app) readSecret(name string) ([]byte, error) {
	var raw []byte
	if f, ok := a.env.Stdin.(*os.File); ok && a.env.IsTerminal(f) {
		fmt.Fprint(a.out.Stderr(), a.p.T(msg.SecretPrompt, i18n.V("name", name)))
		v, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(a.out.Stderr())
		if err != nil {
			return nil, err
		}
		raw = v
	} else {
		r := bufio.NewReader(io.LimitReader(a.env.Stdin, 16<<10))
		line, err := r.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		raw = line
	}
	value := bytes.TrimRight(raw, "\r\n")
	if len(value) == 0 {
		return nil, cliout.Fail(cliout.ExitUsage, "empty_secret", a.p.T(msg.SecretEmpty))
	}
	return value, nil
}

func (a *app) secretSet(ctx context.Context, s *signedIn, name string, value []byte, exp *time.Time) error {
	resp, err := s.client.SetMySecretWithResponse(ctx, name, apiclient.SetSecretRequest{Value: string(value), ExpiresAt: exp})
	if err == nil {
		err = apiclient.Check(resp.HTTPResponse, resp.Body)
	}
	var ae *apiclient.Error
	switch {
	case errors.As(err, &ae) && ae.Status == 404:
		return cliout.Fail(cliout.ExitFailure, "secret_not_used", a.p.T(msg.SecretNotUsed, i18n.V("name", name)))
	case err != nil:
		return a.serverError(s.creds.Server, err)
	}
	doc := secretDoc{Name: name, Configured: true, UsedBy: []string{}}
	if m := resp.JSON200; m != nil {
		doc.Label, doc.UpdatedAt, doc.ExpiresAt = m.Label, m.UpdatedAt, m.ExpiresAt
		for _, r := range m.UsedBy {
			doc.UsedBy = append(doc.UsedBy, r.Code)
		}
	}
	// This computer gets its copy at once, so that the MCP servers can use it now; the others at
	// their next sync.
	_, ferr := s.vault.Refresh(ctx, s.client, s.owner(), []string{name}, time.Now())
	if ferr != nil {
		a.out.Warn(a.p.T(msg.SecretCopyFailed, i18n.V("detail", ferr.Error())))
	}
	if f, err := s.vault.Read(); err == nil {
		if it, ok := f.Find(name); ok {
			doc.Local, doc.Version = true, it.Version
		}
	}
	return a.out.Result(cliout.Schema("secret", 1), doc, func(w io.Writer) error {
		_, err := fmt.Fprintln(w, a.p.T(msg.SecretSetDone, i18n.V("name", name)))
		return err
	})
}

func newSecretListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: a.p.T(msg.SecretListShort),
		Args:  a.noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.signIn()
			if err != nil {
				return err
			}
			resp, err := s.client.ListMySecretsWithResponse(cmd.Context())
			if err == nil {
				err = apiclient.Check(resp.HTTPResponse, resp.Body)
			}
			if err != nil {
				return a.serverError(s.creds.Server, err)
			}
			f, err := s.vault.Read()
			if err != nil {
				return a.vaultError(s.vault, err)
			}
			docs := []secretDoc{}
			if resp.JSON200 != nil {
				for _, m := range resp.JSON200.Items {
					d := secretDoc{Name: m.Name, Label: m.Label, Configured: m.Configured, UpdatedAt: m.UpdatedAt, ExpiresAt: m.ExpiresAt, UsedBy: []string{}}
					if m.Howto != nil {
						d.Howto = *m.Howto
					}
					for _, r := range m.UsedBy {
						d.UsedBy = append(d.UsedBy, r.Code)
					}
					if it, ok := f.Find(m.Name); ok && f.DeviceID == s.creds.DeviceID {
						d.Local, d.Version = true, it.Version
					}
					docs = append(docs, d)
				}
			}
			return a.out.Result(cliout.Schema("secrets", 1), map[string]any{"secrets": docs}, func(w io.Writer) error {
				if len(docs) == 0 {
					_, err := fmt.Fprintln(w, a.p.T(msg.SecretListEmpty))
					return err
				}
				for _, d := range docs {
					state := a.p.T(msg.SecretStateMissing)
					switch {
					case d.Configured && d.Local:
						state = a.p.T(msg.SecretStateSet)
					case d.Configured:
						state = a.p.T(msg.SecretStateNoCopy)
					}
					line := fmt.Sprintf("  %-28s %s", d.Name, state)
					if d.ExpiresAt != nil {
						line += "  " + a.p.T(msg.SecretExpires, i18n.V("date", d.ExpiresAt.Format(time.DateOnly)))
					}
					if len(d.UsedBy) > 0 {
						line += "  " + a.p.T(msg.SecretUsedBy, i18n.V("resources", strings.Join(d.UsedBy, ", ")))
					}
					if _, err := fmt.Fprintln(w, line); err != nil {
						return err
					}
					if !d.Configured && d.Howto != "" {
						if _, err := fmt.Fprintln(w, "      "+a.p.T(msg.SecretHowto, i18n.V("howto", d.Howto))); err != nil {
							return err
						}
					}
				}
				return nil
			})
		},
	}
}

func newSecretRmCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: a.p.T(msg.SecretRmShort),
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := a.secretNameArg(cmd, name); err != nil {
				return err
			}
			s, err := a.signIn()
			if err != nil {
				return err
			}
			resp, err := s.client.DeleteMySecretWithResponse(cmd.Context(), name)
			if err == nil {
				err = apiclient.Check(resp.HTTPResponse, resp.Body)
			}
			var ae *apiclient.Error
			wasSet := true
			switch {
			case errors.As(err, &ae) && ae.Status == 404:
				wasSet = false
			case err != nil:
				return a.serverError(s.creds.Server, err)
			}
			// The local copy goes too, whether or not the server had one.
			if err := s.vault.Remove(name); err != nil {
				return a.vaultError(s.vault, err)
			}
			doc := map[string]any{"name": name, "deleted": wasSet}
			return a.out.Result(cliout.Schema("secret-rm", 1), doc, func(w io.Writer) error {
				line := a.p.T(msg.SecretRmDone, i18n.V("name", name))
				if !wasSet {
					line = a.p.T(msg.SecretRmNotSet, i18n.V("name", name))
				}
				_, err := fmt.Fprintln(w, line)
				return err
			})
		},
	}
}

// newSecretPullCmd: rgd secret pull. It replaces this computer's copies with what the server has
// for the person now: new values arrive, deleted ones go. A sync does the same.
func newSecretPullCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: a.p.T(msg.SecretPullShort),
		Args:  a.noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.signIn()
			if err != nil {
				return err
			}
			got, err := s.vault.Refresh(cmd.Context(), s.client, s.owner(), nil, time.Now())
			if err != nil {
				if errors.Is(err, localsecrets.ErrUnknown) {
					return a.vaultError(s.vault, err)
				}
				return a.serverError(s.creds.Server, err)
			}
			if got.Stored == nil {
				got.Stored = []string{}
			}
			return a.out.Result(cliout.Schema("secret-pull", 1), map[string]any{"stored": got.Stored}, func(w io.Writer) error {
				_, err := fmt.Fprintln(w, a.p.T(msg.SecretPullDone, i18n.Count(len(got.Stored))))
				return err
			})
		},
	}
}
