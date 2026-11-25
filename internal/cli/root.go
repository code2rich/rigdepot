// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
)

// newRoot builds the command tree for one run. Everything people read comes
// from the message catalogue in the language chosen in newApp, including the
// help, the usage errors and the help for the options; cobra's own English
// texts are replaced.
func newRoot(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:               brand.CLI,
		Short:             a.p.T(msg.RootShort),
		Args:              a.subcommandArgs,
		SilenceErrors:     true,
		SilenceUsage:      true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			if v, _ := cmd.Flags().GetBool("version"); v {
				return a.printVersion()
			}
			return a.missingSubcommand(cmd)
		},
	}
	// The values were read by prescan already; these declarations are for
	// parsing, help and completion.
	var g globalFlags
	defineGlobalFlags(root.PersistentFlags(), &g, a.p)
	root.PersistentFlags().BoolP("help", "h", false, a.p.T(msg.FlagHelp))
	root.Flags().Bool("version", false, a.p.T(msg.FlagVersion))
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return a.usageError("bad_flag", a.p.T(msg.ErrorBadFlag, i18n.V("detail", err.Error())), cmd.CommandPath())
	})
	root.SetHelpFunc(func(cmd *cobra.Command, _ []string) {
		if err := a.help(cmd); err != nil {
			a.out.Fail(err)
		}
	})
	root.SetHelpCommand(a.helpCommand())
	// Shown on Windows when the program is started by double-clicking it.
	cobra.MousetrapHelpText = a.p.T(msg.ErrorStartedFromExplorer)
	attach(a, root)
	return root
}

// group returns a command that only holds subcommands, such as "config".
// Run without a subcommand, or with an unknown one, it is a usage error.
func (a *app) group(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  a.subcommandArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.missingSubcommand(cmd)
		},
	}
}

// usageError returns a usage error (exit code 2) with a hint on how to get
// help for the command at path.
func (a *app) usageError(code, message, path string) *cliout.Error {
	if path == "" {
		path = brand.CLI
	}
	e := cliout.Usage(code, message)
	e.Hint = a.p.T(msg.ErrorUsageHint, i18n.V("command", path))
	return e
}

func (a *app) missingSubcommand(cmd *cobra.Command) error {
	return a.usageError("missing_command", a.p.T(msg.ErrorMissingCommand, i18n.V("command", cmd.CommandPath())), cmd.CommandPath())
}

func (a *app) unknownCommand(cmd *cobra.Command, name string) error {
	e := a.usageError("unknown_command",
		a.p.T(msg.ErrorUnknownCommand, i18n.V("name", name), i18n.V("command", cmd.CommandPath())), cmd.CommandPath())
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2 // cobra's default, which SuggestionsFor does not apply itself
	}
	if s := cmd.SuggestionsFor(name); len(s) > 0 {
		e.Hint = a.p.T(msg.ErrorDidYouMean, i18n.V("suggestions", strings.Join(s, ", "))) + "\n" + e.Hint
	}
	return e
}

// subcommandArgs is the argument check of a command with subcommands: an
// argument that reaches it is a subcommand that does not exist.
func (a *app) subcommandArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return a.unknownCommand(cmd, args[0])
	}
	return nil
}

// noArgs accepts no arguments.
func (a *app) noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return a.usageError("unexpected_argument",
			a.p.T(msg.ErrorNoArgs, i18n.V("command", cmd.CommandPath()), i18n.V("arg", args[0])), cmd.CommandPath())
	}
	return nil
}

// exactArgs accepts exactly n arguments.
func (a *app) exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return a.usageError("argument_count",
				a.p.T(msg.ErrorArgCount, i18n.Count(n), i18n.V("command", cmd.CommandPath()), i18n.V("got", len(args))), cmd.CommandPath())
		}
		return nil
	}
}

// explain turns what the command tree returned into an error the person
// can read. Errors from commands are already *cliout.Error or
// cliout.Status; anything else is a defect and reported as internal.
func (a *app) explain(err error) error {
	if err == nil {
		return nil
	}
	var e *cliout.Error
	var s cliout.Status
	if errors.As(err, &e) || errors.As(err, &s) {
		return err
	}
	ie := cliout.Fail(cliout.ExitFailure, "internal", a.p.T(msg.ErrorInternal, i18n.V("detail", err.Error())))
	ie.Err = err
	return ie
}

// helpCommand replaces cobra's "help" command, whose text is English.
func (a *app) helpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: a.p.T(msg.HelpShort),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, rest, err := cmd.Root().Find(args)
			if err != nil || target == nil {
				return a.unknownCommand(cmd.Root(), args[0])
			}
			if len(rest) > 0 {
				return a.unknownCommand(target, rest[0])
			}
			return a.help(target)
		},
	}
}

// help writes the help of a command: to standard output, or with --json as
// the document {"schema":"rgd.help/v1","command":…,"text":…}.
func (a *app) help(cmd *cobra.Command) error {
	text := a.helpText(cmd)
	data := struct {
		Command string `json:"command"`
		Text    string `json:"text"`
	}{cmd.CommandPath(), text}
	return a.out.Result(cliout.Schema("help", 1), data, func(w io.Writer) error {
		_, err := io.WriteString(w, text)
		return err
	})
}

func (a *app) helpText(cmd *cobra.Command) string {
	var b strings.Builder
	b.WriteString(cmd.Short + "\n\n")
	b.WriteString(a.p.T(msg.HelpUsage) + "\n")
	line := cmd.CommandPath()
	if _, rest, ok := strings.Cut(cmd.Use, " "); ok {
		line += " " + rest
	}
	if cmd.HasAvailableSubCommands() && !strings.Contains(cmd.Use, " ") {
		line += " <command>"
	}
	b.WriteString("  " + line + "\n")
	if cmd.Long != "" {
		b.WriteString("\n" + strings.TrimRight(cmd.Long, "\n") + "\n")
	}
	if cmd.HasAvailableSubCommands() {
		b.WriteString("\n" + a.p.T(msg.HelpCommands) + "\n")
		width := 0
		for _, c := range cmd.Commands() {
			if c.IsAvailableCommand() || c.Name() == "help" {
				width = max(width, cliout.DisplayWidth(c.Name()))
			}
		}
		for _, c := range cmd.Commands() {
			if c.IsAvailableCommand() || c.Name() == "help" {
				fmt.Fprintf(&b, "  %s   %s\n", cliout.PadRight(c.Name(), width), c.Short)
			}
		}
	}
	local, global := cmd.LocalNonPersistentFlags(), cmd.InheritedFlags()
	if !cmd.HasParent() {
		global = cmd.PersistentFlags()
	}
	if s := flagUsages(local); s != "" {
		b.WriteString("\n" + a.p.T(msg.HelpFlags) + "\n" + s)
	}
	if s := flagUsages(global); s != "" {
		b.WriteString("\n" + a.p.T(msg.HelpGlobalFlags) + "\n" + s)
	}
	if cmd.HasAvailableSubCommands() {
		b.WriteString("\n" + a.p.T(msg.HelpMore, i18n.V("command", cmd.CommandPath())) + "\n")
	}
	return b.String()
}

func flagUsages(fs *pflag.FlagSet) string {
	if fs == nil || !fs.HasAvailableFlags() {
		return ""
	}
	return fs.FlagUsages()
}
