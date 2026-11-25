// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/buildinfo"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
)

func init() { register("", newVersionCmd) }

// newVersionCmd: rgd version. It needs neither the server nor the local
// configuration, so it works when everything else does not.
func newVersionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: a.p.T(msg.VersionShort),
		Args:  a.noArgs,
		RunE: func(*cobra.Command, []string) error {
			return a.printVersion()
		},
	}
}

// versionDoc is the JSON output of rgd version (schema rgd.version/v1).
type versionDoc struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Date     string `json:"date"`
	Modified bool   `json:"modified"`
	Go       string `json:"go"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

// printVersion is shared by "rgd version" and "rgd --version". The human line
// is the same in every language: it is what people paste into issues.
func (a *app) printVersion() error {
	info := buildinfo.Get()
	doc := versionDoc{info.Version, info.Commit, info.Date, info.Modified, info.GoVersion, info.OS, info.Arch}
	return a.out.Result(cliout.Schema("version", 1), doc, func(w io.Writer) error {
		_, err := fmt.Fprintln(w, info.String(brand.CLI))
		return err
	})
}
