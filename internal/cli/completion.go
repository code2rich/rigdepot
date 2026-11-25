// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"bytes"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
)

func init() { register("", newCompletionCmd) }

// shells are the shells "rgd completion" writes a script for.
var shells = []string{"bash", "zsh", "fish", "powershell"}

// newCompletionCmd: rgd completion <shell>. It replaces cobra's own
// completion command, whose help is English. The scripts call the hidden
// command cobra adds for completion at run time, so they complete whatever
// commands this build has.
func newCompletionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:       "completion <shell>",
		Short:     a.p.T(msg.CompletionShort),
		Long:      a.p.T(msg.CompletionLong),
		ValidArgs: shells,
		Args:      a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var script bytes.Buffer
			root := cmd.Root()
			var err error
			switch args[0] {
			case "bash":
				err = root.GenBashCompletionV2(&script, true)
			case "zsh":
				err = root.GenZshCompletion(&script)
			case "fish":
				err = root.GenFishCompletion(&script, true)
			case "powershell":
				err = root.GenPowerShellCompletionWithDesc(&script)
			default:
				return a.usageError("unknown_shell",
					a.p.T(msg.CompletionUnknownShell, i18n.V("shell", args[0]), i18n.V("shells", strings.Join(shells, ", "))),
					cmd.CommandPath())
			}
			if err != nil {
				return err
			}
			doc := struct {
				Shell  string `json:"shell"`
				Script string `json:"script"`
			}{args[0], script.String()}
			return a.out.Result(cliout.Schema("completion", 1), doc, func(w io.Writer) error {
				_, err := w.Write(script.Bytes())
				return err
			})
		},
	}
}
