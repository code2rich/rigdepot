// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package mcprun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"slices"
	"strings"

	"github.com/code2rich/rigdepot/internal/brand"
)

// SecretSource returns the values of the named personal secrets. It fails with a
// *MissingSecretsError when some cannot be had because they are not set (or this computer is not
// signed in); any other error (a keychain that cannot be read, copies that do not open) is
// reported as it is and is not a reason to make anything new.
type SecretSource func(names []string) (map[string][]byte, error)

// MissingSecretsError says which secrets a server needs and this computer does not have.
type MissingSecretsError struct {
	Server string
	Names  []string
	// NotSignedIn: the computer is not signed in, so it has no secrets at all.
	NotSignedIn bool
}

func (e *MissingSecretsError) Error() string {
	if e.NotSignedIn {
		return fmt.Sprintf("the MCP server %s needs the personal secrets %s, and this computer is not signed in (run %q in a terminal)",
			e.Server, strings.Join(e.Names, ", "), brand.CLI+" login")
	}
	return fmt.Sprintf("the MCP server %s needs the personal secrets %s, which are not set on this computer: run %q in a terminal, "+
		"or set them on the server's \"Keys and devices\" page and run %q; never paste a secret into this conversation",
		e.Server, strings.Join(e.Names, ", "), brand.CLI+" secret set "+e.Names[0], brand.CLI+" secret pull")
}

// Options are what Run, Serve and Call share.
type Options struct {
	// Secrets gives the values of personal secrets; nil has none.
	Secrets SecretSource
	// Explain turns an error about a server, a *MissingSecretsError above all, into the text
	// the AI and the person read. Nil uses the error's own text.
	Explain func(error) string
	// Stdin, Stdout and Stderr are the streams to the AI tool (Run and Serve) or the terminal
	// (Call). Stderr also receives what stdio servers write to their standard error.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Version is this program's version, for the MCP handshake.
	Version string
	// HTTP is the client for HTTP servers; nil is http.DefaultClient.
	HTTP *http.Client
}

func (o *Options) explain(err error) string {
	if o.Explain != nil {
		return o.Explain(err)
	}
	return err.Error()
}

func (o *Options) http() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return http.DefaultClient
}

// secretsFor returns the secrets server s needs.
func (o *Options) secretsFor(s Server) (map[string][]byte, error) {
	names := s.Secrets()
	if len(names) == 0 {
		return map[string][]byte{}, nil
	}
	if o.Secrets == nil {
		return nil, &MissingSecretsError{Server: s.Code, Names: names, NotSignedIn: true}
	}
	vals, err := o.Secrets(names)
	var missing *MissingSecretsError
	if errors.As(err, &missing) {
		missing.Server = s.Code
		if len(missing.Names) == 0 {
			missing.Names = names
		}
		return nil, missing
	}
	return vals, err
}

func wipe(secrets map[string][]byte) {
	for _, v := range secrets {
		clear(v)
	}
}

// ExitError is a stdio server that ended with a status other than 0.
type ExitError struct{ Code int }

func (e *ExitError) Error() string {
	return fmt.Sprintf("the MCP server exited with status %d", e.Code)
}

// Run connects the AI tool on o's streams to server s (rgd mcp run). A stdio server is started
// with the tool's streams as its own; an HTTP one is bridged. When the secrets s needs are
// missing, or cannot be read, Run serves a stand-in that answers with what is wrong instead,
// until the tool closes the connection, and also says it on Stderr.
func Run(ctx context.Context, s Server, o Options) error {
	o = o.shared()
	secrets, err := o.secretsFor(s)
	if err != nil {
		text := o.explain(err)
		fmt.Fprintln(o.Stderr, text)
		return serveStandIn(ctx, s, text, o)
	}
	defer wipe(secrets)
	if s.Transport == Stdio {
		cmd := exec.CommandContext(ctx, s.Command, s.Args...)
		cmd.Env = environ(s, secrets)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = o.Stdin, o.Stdout, o.Stderr
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return &ExitError{Code: ee.ExitCode()}
		}
		return err
	}
	c, err := connect(ctx, s, secrets, o.Stderr, o.http())
	if err != nil {
		text := o.explain(err)
		fmt.Fprintln(o.Stderr, text)
		return serveStandIn(ctx, s, text, o)
	}
	return bridge(ctx, c, o)
}

// bridge copies messages from the tool to the server and back until either side ends.
func bridge(ctx context.Context, c conn, o Options) error {
	out := &lineWriter{w: o.Stdout}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for msg := range c.recv() {
			if err := out.write(msg); err != nil {
				return
			}
		}
	}()
	err := readLines(o.Stdin, func(line []byte) {
		if !json.Valid(line) {
			out.write(errorResponse(nil, codeParseError, "not JSON"))
			return
		}
		if err := c.send(ctx, line); err != nil {
			var m message
			if json.Unmarshal(line, &m) == nil && m.isRequest() {
				out.write(errorResponse(m.ID, codeServer, "the MCP server cannot be reached: "+err.Error()))
			}
		}
	})
	c.close()
	<-done
	return err
}

// serveStandIn answers the tool in place of a server that cannot be reached: the handshake
// works, the server's listed tools are offered, and every call answers with text, the reason.
func serveStandIn(ctx context.Context, s Server, text string, o Options) error {
	out := &lineWriter{w: o.Stdout}
	tools := standInTools(s, sanitize, text)
	return readLines(o.Stdin, func(line []byte) {
		if ctx.Err() != nil {
			return
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			out.write(errorResponse(nil, codeParseError, "not JSON"))
			return
		}
		if !m.isRequest() {
			return
		}
		switch m.Method {
		case "initialize":
			out.write(response(m.ID, initializeResult(m.Params, s.Code, o.Version, text)))
		case "ping":
			out.write(response(m.ID, struct{}{}))
		case "tools/list":
			out.write(response(m.ID, map[string]any{"tools": tools}))
		case "tools/call":
			out.write(response(m.ID, toolError(text)))
		default:
			out.write(errorResponse(m.ID, codeMethodNotFound, "not supported"))
		}
	})
}

// standInTools are the tools offered for a server that cannot be reached: the ones its
// repository lists, or a single one named "unavailable", each described by text and named by
// name.
func standInTools(s Server, name func(string) string, text string) []map[string]any {
	schema := map[string]any{"type": "object"}
	var out []map[string]any
	for _, t := range s.Tools {
		out = append(out, map[string]any{"name": name(t.Name), "description": "Unavailable: " + text, "inputSchema": schema})
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"name": name("unavailable"), "description": "Unavailable: " + text, "inputSchema": schema})
	}
	return out
}

// toolError is a tool result that reports an error to the AI.
func toolError(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": true}
}

// initializeResult answers an initialize request with the version the client asked for when
// this package knows it.
func initializeResult(params json.RawMessage, name, version, instructions string) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	json.Unmarshal(params, &p)
	v := ProtocolVersion
	if slices.Contains(protocolVersions, p.ProtocolVersion) {
		v = p.ProtocolVersion
	}
	r := map[string]any{
		"protocolVersion": v,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": name, "version": version},
	}
	if instructions != "" {
		r["instructions"] = instructions
	}
	return r
}
