// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/device"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/localconfig"
	"github.com/code2rich/rigdepot/internal/localsecrets"
	"github.com/code2rich/rigdepot/internal/mcprun"
)

func init() {
	register("", newMCPCmd)
	register("mcp", newMCPRunCmd)
	register("mcp", newMCPServeCmd)
	register("mcp", newMCPCallCmd)
}

// newMCPCmd: rgd mcp, the MCP servers synced to this computer, with personal secrets filled in
// on the person's computer at the moment of use.
func newMCPCmd(a *app) *cobra.Command {
	return a.group("mcp", a.p.T(msg.MCPShort))
}

// mcpOptions are how sync and status write MCP servers into the tools. Without a server there
// are no personal secrets to fill in, so the tools' own environment variable references are the
// only way (decision D5); with one, mcp.mode decides: the single entry (rgd mcp serve) or one
// entry per server started through rgd mcp run.
func (a *app) mcpOptions(cfg *localconfig.Config) (adapter.MCPMode, string) {
	if cfg.String(localconfig.KeyServer) == "" {
		return adapter.MCPNativeEnv, ""
	}
	mode := adapter.MCPSingleEntry
	if cfg.String(localconfig.KeyMCPMode) == "direct" {
		mode = adapter.MCPLauncher
	}
	return mode, a.launcher(cfg)
}

// launcher is the command written into the tools' configuration to start this program: its name
// (mcp.command = name), or its absolute path. The path is the one found on PATH when that is this
// program, so that a package manager's link (which survives upgrades) is written rather than
// the versioned file it points at.
func (a *app) launcher(cfg *localconfig.Config) string {
	if cfg.String(localconfig.KeyMCPCommand) == "name" {
		return brand.CLI
	}
	executable := a.env.Executable
	if executable == nil {
		executable = os.Executable
	}
	self, err := executable()
	if err != nil {
		return brand.CLI
	}
	if onPath, err := exec.LookPath(brand.CLI); err == nil {
		if abs, err := filepath.Abs(onPath); err == nil {
			a1, err1 := os.Stat(abs)
			a2, err2 := os.Stat(self)
			if err1 == nil && err2 == nil && os.SameFile(a1, a2) {
				return abs
			}
		}
	}
	return self
}

// mcpSetup is what the three commands share: the local data directory, the device identity and
// the options of package mcprun.
func (a *app) mcpSetup() (home.Dir, mcprun.Options, error) {
	id, data, err := a.identity()
	if err != nil {
		return home.Dir{}, mcprun.Options{}, err
	}
	o := mcprun.Options{
		Secrets: a.mcpSecrets(id, data),
		Explain: a.explainMCP,
		Stdin:   a.env.Stdin,
		Stdout:  a.env.Stdout,
		Stderr:  a.env.Stderr,
		Version: apiclient.Version(),
	}
	return data, o, nil
}

// mcpSecrets opens the sealed copies of this computer's personal secrets with the device key.
// It never makes a key: a keychain that cannot be read is reported as it is (device.StoreError).
func (a *app) mcpSecrets(id *device.Identity, data home.Dir) mcprun.SecretSource {
	return func(names []string) (map[string][]byte, error) {
		creds, err := id.Credentials()
		if errors.Is(err, device.ErrNotLoggedIn) {
			return nil, &mcprun.MissingSecretsError{Names: names, NotSignedIn: true}
		}
		if err != nil {
			return nil, err
		}
		key, err := id.PrivateKey()
		if errors.Is(err, device.ErrNotLoggedIn) {
			return nil, &mcprun.MissingSecretsError{Names: names, NotSignedIn: true}
		}
		if err != nil {
			return nil, err
		}
		owner := localsecrets.Owner{Server: creds.Server, UserID: creds.UserID, DeviceID: creds.DeviceID}
		vals, err := localsecrets.New(data).Open(key, owner, names)
		var miss *localsecrets.MissingError
		if errors.As(err, &miss) {
			return nil, &mcprun.MissingSecretsError{Names: miss.Names}
		}
		return vals, err
	}
}

// explainMCP is the text the AI and the person read about a server that cannot be used.
func (a *app) explainMCP(err error) string {
	var miss *mcprun.MissingSecretsError
	var se *device.StoreError
	switch {
	case errors.As(err, &miss) && miss.NotSignedIn:
		return a.p.T(msg.MCPMissingNotSignedIn, i18n.V("server", miss.Server), i18n.V("names", strings.Join(miss.Names, ", ")))
	case errors.As(err, &miss):
		return a.p.T(msg.MCPMissingSecrets, i18n.V("server", miss.Server), i18n.V("names", strings.Join(miss.Names, ", ")), i18n.V("first", miss.Names[0]))
	case errors.As(err, &se):
		return a.p.T(msg.MCPKeyringUnknown, i18n.V("detail", se.Err.Error()))
	case errors.Is(err, localsecrets.ErrNotYours), errors.Is(err, localsecrets.ErrOpen):
		return a.p.T(msg.MCPCopiesUnusable, i18n.V("detail", err.Error()))
	case errors.Is(err, localsecrets.ErrUnknown):
		return a.p.T(msg.MCPCopiesUnreadable, i18n.V("detail", err.Error()))
	}
	return err.Error()
}

// mcpLoadError explains a server that is not on this computer, or whose file cannot be read.
func (a *app) mcpLoadError(code string, err error) error {
	if errors.Is(err, mcprun.ErrUnknownServer) {
		e := cliout.Fail(cliout.ExitFailure, "mcp_unknown", a.p.T(msg.MCPUnknown, i18n.V("resource", code)))
		e.Hint = a.p.T(msg.MCPUnknownHint)
		return e
	}
	e := cliout.Fail(cliout.ExitStateUnknown, "mcp_unreadable", a.p.T(msg.MCPUnreadable, i18n.V("detail", err.Error())))
	e.Hint = a.p.T(msg.ErrorRunDoctor)
	e.Err = err
	return e
}

// newMCPRunCmd: rgd mcp run <code>. What the tools' configuration starts for one MCP server
// (mcp.mode = direct). Its standard input and output are the MCP connection, so it prints
// nothing else there.
func newMCPRunCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "run <resource>",
		Short: a.p.T(msg.MCPRunShort),
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, o, err := a.mcpSetup()
			if err != nil {
				return err
			}
			s, err := mcprun.Load(data, args[0])
			if err != nil {
				return a.mcpLoadError(args[0], err)
			}
			err = mcprun.Run(cmd.Context(), s, o)
			var ee *mcprun.ExitError
			if errors.As(err, &ee) {
				return cliout.Fail(cliout.ExitFailure, "mcp_exited", err.Error())
			}
			return err
		},
	}
}

// newMCPServeCmd: rgd mcp serve. The single entry: one MCP server on standard input and output
// with the tools of every MCP server synced to this computer.
func newMCPServeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: a.p.T(msg.MCPServeShort),
		Args:  a.noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, o, err := a.mcpSetup()
			if err != nil {
				return err
			}
			servers, err := mcprun.LoadAll(data)
			if err != nil {
				return a.mcpLoadError("", err)
			}
			return mcprun.Serve(cmd.Context(), servers, o)
		},
	}
}

// mcpCallDoc is the JSON output of rgd mcp call (schema rgd.mcp-call/v1): the tool's result as
// the server wrote it, or the server's tools.
type mcpCallDoc struct {
	Server string          `json:"server"`
	Tool   string          `json:"tool,omitempty"`
	Result json.RawMessage `json:"result"`
}

// newMCPCallCmd: rgd mcp call <code> [tool] ['<JSON>']. One call of one tool, for servers
// delivered on demand: the skill tells the AI to run it. Without a tool it lists the server's
// tools with their arguments.
//
// Exit codes: 0 the tool answered; 1 the tool reported an error (its answer is printed all the
// same), the server is not on this computer or failed; 2 the arguments are not a JSON object;
// 3 a personal secret the server needs is missing; 9 the keychain or the local copies cannot be
// read.
func newMCPCallCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "call <resource> [tool] ['<arguments as JSON>']",
		Short: a.p.T(msg.MCPCallShort),
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return a.exactArgs(1)(cmd, args)
			}
			return a.maxArgs(3)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			data, o, err := a.mcpSetup()
			if err != nil {
				return err
			}
			code, tool := args[0], ""
			var raw json.RawMessage
			if len(args) > 1 {
				tool = args[1]
			}
			if len(args) > 2 {
				raw = json.RawMessage(args[2])
			}
			s, err := mcprun.Load(data, code)
			if err != nil {
				return a.mcpLoadError(code, err)
			}
			result, err := mcprun.Call(cmd.Context(), s, tool, raw, o)
			var miss *mcprun.MissingSecretsError
			var se *device.StoreError
			var te *mcprun.ToolError
			switch {
			case errors.Is(err, mcprun.ErrNotJSON):
				return a.usageError("bad_arguments", a.p.T(msg.MCPCallNotJSON), cmd.CommandPath())
			case errors.As(err, &miss):
				return cliout.Fail(cliout.ExitPending, "secret_missing", a.explainMCP(err))
			case errors.As(err, &se), errors.Is(err, localsecrets.ErrUnknown):
				e := cliout.Fail(cliout.ExitStateUnknown, "keyring_unknown", a.explainMCP(err))
				e.Err = err
				return e
			case errors.As(err, &te):
			case err != nil:
				e := cliout.Fail(cliout.ExitFailure, "mcp_failed", a.p.T(msg.MCPCallFailed, i18n.V("resource", code), i18n.V("detail", a.explainMCP(err))))
				e.Err = err
				return e
			}
			doc := mcpCallDoc{Server: code, Tool: tool, Result: result}
			if werr := a.out.Result(cliout.Schema("mcp-call", 1), doc, func(w io.Writer) error {
				if tool == "" {
					return writeTools(w, result)
				}
				return writeToolResult(w, result)
			}); werr != nil {
				return werr
			}
			if te != nil {
				return cliout.Fail(cliout.ExitFailure, "tool_error", a.p.T(msg.MCPCallToolError, i18n.V("tool", tool)))
			}
			return nil
		},
	}
}

// writeToolResult prints a tool's result for the AI: the text of each part as it is, anything
// else (an image, a resource) as JSON on a line of its own, and the structured content when
// there is no text.
func writeToolResult(w io.Writer, raw json.RawMessage) error {
	var r struct {
		Content []json.RawMessage `json:"content"`
		Struct  json.RawMessage   `json:"structuredContent"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		_, err := fmt.Fprintln(w, string(raw))
		return err
	}
	wrote := false
	for _, c := range r.Content {
		var part struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		json.Unmarshal(c, &part)
		line := string(c)
		if part.Type == "text" {
			line = part.Text
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
		wrote = true
	}
	if !wrote && len(r.Struct) > 0 {
		_, err := fmt.Fprintln(w, string(r.Struct))
		return err
	}
	return nil
}

// writeTools prints a server's tools: the name, what it does and its arguments' schema, which is
// what the AI needs to call one.
func writeTools(w io.Writer, raw json.RawMessage) error {
	var r struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	for _, t := range r.Tools {
		if _, err := fmt.Fprintf(w, "%s\n  %s\n  arguments: %s\n", t.Name, strings.ReplaceAll(strings.TrimSpace(t.Description), "\n", "\n  "), compact(t.InputSchema)); err != nil {
			return err
		}
	}
	return nil
}

func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if len(raw) == 0 {
		return "{}"
	}
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}
