// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package mcprun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrNotJSON: the arguments of a call are not a JSON object.
var ErrNotJSON = errors.New("the arguments are not a JSON object")

// ToolError is a call the tool answered with an error (isError): the result says what.
type ToolError struct{ Result json.RawMessage }

func (e *ToolError) Error() string { return "the tool reported an error" }

// Call connects to server s, calls one tool with the arguments (a JSON object; empty is {}) and
// returns the result as the server wrote it (rgd mcp call). A result marked as an error comes
// back too, with a *ToolError. Without a tool it returns the server's tools instead, as
// {"tools": [...]}, so that the AI can see their arguments.
//
// A missing secret is a *MissingSecretsError, returned before anything is started.
func Call(ctx context.Context, s Server, tool string, args json.RawMessage, o Options) (json.RawMessage, error) {
	o = o.shared()
	if len(args) > 0 {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(args, &obj); err != nil || obj == nil {
			return nil, ErrNotJSON
		}
	}
	secrets, err := o.secretsFor(s)
	if err != nil {
		return nil, err
	}
	sctx, cancel := context.WithTimeout(ctx, StartTimeout)
	defer cancel()
	c, err := connect(sctx, s, secrets, o.Stderr, o.http())
	wipe(secrets)
	if err != nil {
		return nil, err
	}
	cl := newClient(c)
	defer cl.close()
	if err := cl.initialize(sctx, o.Version); err != nil {
		return nil, fmt.Errorf("the MCP server %s did not start: %w", s.Code, err)
	}
	if tool == "" {
		tools, err := cl.listTools(sctx)
		if err != nil {
			return nil, err
		}
		if tools == nil {
			tools = []map[string]any{}
		}
		return json.Marshal(map[string]any{"tools": tools})
	}
	raw, err := cl.callTool(ctx, tool, args)
	if err != nil {
		return nil, err
	}
	var r struct {
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(raw, &r) == nil && r.IsError {
		return raw, &ToolError{Result: raw}
	}
	return raw, nil
}
