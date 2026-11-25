// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/internal/i18n/msg"
)

func init() { register("", newDisableCmd) }

// newDisableCmd: rgd disable <resource>. It turns off a recommended resource, or an optional one
// the person had turned on, in the local selection (state/selection.json); the next sync removes
// it. A required resource cannot be turned off: the command refuses and says so.
func newDisableCmd(a *app) *cobra.Command {
	var src sourceFlags
	cmd := &cobra.Command{
		Use:   "disable <resource>",
		Short: a.p.T(msg.DisableShort),
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.changeSelection(cmd.Context(), cmd, src, args[0], false)
		},
	}
	a.addSourceFlags(cmd, &src)
	return cmd
}
