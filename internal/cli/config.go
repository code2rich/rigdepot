// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/localconfig"
)

func init() {
	register("", newConfigCmd)
	register("config", newConfigGetCmd)
	register("config", newConfigSetCmd)
	register("config", newConfigListCmd)
}

// newConfigCmd: rgd config get|set|list, the local configuration in
// ~/.rigdepot/config.toml and what overrides it.
func newConfigCmd(a *app) *cobra.Command {
	return a.group("config", a.p.T(msg.ConfigShort))
}

// settingDoc is one setting in the JSON output: the value is a string, a
// list of strings, or null when unset. origin names the environment variable
// or option the value came from.
type settingDoc struct {
	Key    string  `json:"key"`
	Value  any     `json:"value"`
	Source string  `json:"source"`
	Origin *string `json:"origin,omitempty"`
}

func newSettingDoc(v localconfig.Value) settingDoc {
	d := settingDoc{Key: v.Key, Source: v.Source.String()}
	switch {
	case v.IsList():
		d.Value = append([]string{}, v.List...)
	case v.IsSet():
		d.Value = v.Str
	}
	if v.Origin != "" {
		origin := v.Origin
		d.Origin = &origin
	}
	return d
}

func newConfigGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: a.p.T(msg.ConfigGetShort),
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.setting(cmd, args[0])
			if err != nil {
				return err
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			v := cfg.Get(s.Key)
			return a.out.Result(cliout.Schema("setting", 1), newSettingDoc(v), func(w io.Writer) error {
				if !v.IsSet() {
					return nil
				}
				_, err := fmt.Fprintln(w, v.Display())
				return err
			})
		},
	}
}

func newConfigListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: a.p.T(msg.ConfigListShort),
		Args:  a.noArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			doc := struct {
				File     string       `json:"file"`
				Settings []settingDoc `json:"settings"`
			}{File: cfg.Path()}
			var values []localconfig.Value
			for _, s := range localconfig.Settings() {
				if s.FlagOnly {
					continue // not configuration: a decision made on the command line each time
				}
				v := cfg.Get(s.Key)
				values = append(values, v)
				doc.Settings = append(doc.Settings, newSettingDoc(v))
			}
			return a.out.Result(cliout.Schema("config", 1), doc, func(w io.Writer) error {
				fmt.Fprintln(w, a.p.T(msg.ConfigListFile, i18n.V("path", a.showPath(cfg.Path()))))
				width := 0
				for _, v := range values {
					width = max(width, cliout.DisplayWidth(v.Key))
				}
				shown := make([]string, len(values))
				valueWidth := 0
				for i, v := range values {
					shown[i] = v.Display()
					if !v.IsSet() {
						shown[i] = a.p.T(msg.ConfigValueUnset)
					}
					valueWidth = max(valueWidth, cliout.DisplayWidth(shown[i]))
				}
				for i, v := range values {
					_, err := fmt.Fprintf(w, "  %s  %s  %s\n", cliout.PadRight(v.Key, width),
						cliout.PadRight(shown[i], valueWidth), a.out.Paint(cliout.Dim, a.sourceText(v)))
					if err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
}

func newConfigSetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: a.p.T(msg.ConfigSetShort),
		Long:  a.p.T(msg.ConfigSetLong),
		Args:  a.exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.setting(cmd, args[0])
			if err != nil {
				return err
			}
			if a.home == "" {
				return a.noHomeError()
			}
			path := a.cfg.Path()
			v, err := localconfig.SaveFile(path, s.Key, args[1])
			if err != nil {
				return a.saveError(cmd, s, err)
			}
			// The file is changed, but a flag or variable may still win.
			if now := a.cfg.Get(s.Key); now.Source > localconfig.SourceFile {
				a.out.Info(a.p.T(msg.ConfigSetOverridden, i18n.V("key", s.Key), i18n.V("origin", now.Origin)))
			}
			doc := struct {
				settingDoc
				File string `json:"file"`
			}{newSettingDoc(v), path}
			return a.out.Result(cliout.Schema("setting", 1), doc, func(w io.Writer) error {
				var line string
				if v.Source == localconfig.SourceFile {
					line = a.p.T(msg.ConfigSetDone, i18n.V("key", s.Key), i18n.V("value", v.Display()), i18n.V("path", a.showPath(path)))
				} else {
					line = a.p.T(msg.ConfigSetRemoved, i18n.V("key", s.Key), i18n.V("path", a.showPath(path)))
				}
				_, err := fmt.Fprintln(w, line)
				return err
			})
		},
	}
}

// setting looks up a key typed by the person. Flag-only settings are not
// configuration and are refused with an explanation.
func (a *app) setting(cmd *cobra.Command, key string) (localconfig.Setting, error) {
	s, ok := localconfig.Lookup(key)
	if !ok {
		return s, a.usageError("unknown_key", a.p.T(msg.ConfigUnknownKey, i18n.V("key", key)), cmd.CommandPath())
	}
	if s.FlagOnly {
		return s, a.usageError("flag_only", a.p.T(msg.ConfigFlagOnly, i18n.V("key", key), i18n.V("flag", s.Flag)), cmd.CommandPath())
	}
	return s, nil
}

// saveError explains why config set did not write.
func (a *app) saveError(cmd *cobra.Command, s localconfig.Setting, err error) error {
	var (
		nf *localconfig.NotInFileError
		ve *localconfig.ValueError
		fe *localconfig.FileError
	)
	switch {
	case errors.As(err, &nf):
		ways := append([]string(nil), s.Env...)
		if s.Flag != "" {
			ways = append([]string{s.Flag}, ways...)
		}
		return a.usageError("not_in_file",
			a.p.T(msg.ConfigNotInFile, i18n.V("key", s.Key), i18n.V("ways", strings.Join(ways, ", "))), cmd.CommandPath())
	case errors.As(err, &ve):
		return a.usageError("bad_value",
			a.p.T(msg.ConfigBadValue, i18n.V("key", s.Key), i18n.V("value", ve.Value), i18n.V("reason", a.reason(ve))), cmd.CommandPath())
	case errors.As(err, &fe):
		e := cliout.Fail(cliout.ExitFailure, "config_unreadable",
			a.p.T(msg.ErrorConfigUnreadable, i18n.V("path", a.showPath(fe.Path)), i18n.V("detail", fe.Err.Error())))
		e.Err = err
		return e
	}
	e := cliout.Fail(cliout.ExitFailure, "config_write",
		a.p.T(msg.ErrorConfigWrite, i18n.V("path", a.showPath(a.cfg.Path())), i18n.V("detail", err.Error())))
	e.Err = err
	return e
}

// reason explains in the person's language why a value was refused.
func (a *app) reason(ve *localconfig.ValueError) string {
	switch ve.Reason {
	case localconfig.ReasonEnum, localconfig.ReasonBool:
		return a.p.T(msg.ConfigReasonEnum, i18n.V("values", strings.Join(ve.Allowed, ", ")))
	case localconfig.ReasonURL:
		return a.p.T(msg.ConfigReasonURL)
	case localconfig.ReasonLang:
		return a.p.T(msg.ConfigReasonLang)
	}
	return string(ve.Reason)
}

// sourceText says where a value came from, for people.
func (a *app) sourceText(v localconfig.Value) string {
	switch v.Source {
	case localconfig.SourceFlag:
		return a.p.T(msg.ConfigSourceFlag, i18n.V("name", v.Origin))
	case localconfig.SourceEnv:
		return a.p.T(msg.ConfigSourceEnv, i18n.V("name", v.Origin))
	case localconfig.SourceFile:
		return a.p.T(msg.ConfigSourceFile)
	}
	return a.p.T(msg.ConfigSourceDefault)
}
