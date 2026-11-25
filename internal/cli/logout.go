// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/device"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/localsecrets"
)

func init() { register("", newLogoutCmd) }

// newLogoutCmd: rgd logout. It signs this computer out on the server and deletes the device token
// and key from the keychain. When the server cannot be told, the token is deleted all the same and
// the person is told how to sign the computer out on the server's page. The local copies of
// personal secrets (secrets/vault.json, package localsecrets) go with the key they are sealed
// to. The server's address stays in config.toml for the next rgd login.
func newLogoutCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: a.p.T(msg.LogoutShort),
		Args:  a.noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			id, data, err := a.identity()
			if err != nil {
				return err
			}
			out, err := id.Logout(cmd.Context(), func(c device.Credentials) (device.API, error) {
				return a.connect(cfg, c.Server, c.Token)
			})
			if err != nil {
				return a.deviceError(out.Credentials.Server, err)
			}
			// The key they were sealed to is gone, so are the copies. A key that could not be
			// deleted stopped Logout above, and the copies stay with it.
			if err := localsecrets.New(data).Delete(); err != nil {
				return err
			}
			doc := logoutDoc{WasSignedIn: out.SignedIn, Server: out.Credentials.Server, ServerSignedOut: out.SignedIn && out.ServerErr == nil}
			if out.ServerErr != nil {
				a.out.Warn(a.p.T(msg.LogoutServerFailed, i18n.V("address", out.Credentials.Server), i18n.V("detail", out.ServerErr.Error())))
			}
			return a.out.Result(cliout.Schema("logout", 1), doc, func(w io.Writer) error {
				line := a.p.T(msg.LogoutNotSignedIn)
				if out.SignedIn {
					line = a.p.T(msg.LogoutDone, i18n.V("address", out.Credentials.Server))
				}
				_, err := fmt.Fprintln(w, line)
				return err
			})
		},
	}
}

// logoutDoc is the JSON output of rgd logout (schema rgd.logout/v1).
type logoutDoc struct {
	// WasSignedIn says whether there was a sign-in to end.
	WasSignedIn bool   `json:"wasSignedIn"`
	Server      string `json:"server,omitempty"`
	// ServerSignedOut says whether the server signed the device out (or its token no longer
	// worked). False with WasSignedIn: the token is deleted here, but the device may still be
	// signed in on the server.
	ServerSignedOut bool `json:"serverSignedOut"`
}
