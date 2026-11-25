// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package mcprun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/code2rich/rigdepot/internal/brand"
)

// client speaks MCP to one upstream server as its client.
type client struct {
	c       conn
	next    atomic.Int64
	mu      sync.Mutex
	pending map[string]chan message
	done    chan struct{}
}

// errClosed: the upstream server went away.
var errClosed = errors.New("the MCP server closed the connection")

func newClient(c conn) *client {
	cl := &client{c: c, pending: map[string]chan message{}, done: make(chan struct{})}
	go cl.dispatch()
	return cl
}

// dispatch reads what the server sends: answers go to whoever waits for them; requests of the
// server are answered at once, since this client offers nothing (no sampling, no roots), except
// for ping; notifications are dropped.
func (cl *client) dispatch() {
	defer close(cl.done)
	for raw := range cl.c.recv() {
		var m message
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		switch {
		case m.isResponse():
			cl.mu.Lock()
			ch := cl.pending[string(m.ID)]
			delete(cl.pending, string(m.ID))
			cl.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case m.isRequest():
			var reply []byte
			if m.Method == "ping" {
				reply = response(m.ID, struct{}{})
			} else {
				reply = errorResponse(m.ID, codeMethodNotFound, "not supported by "+brand.CLI)
			}
			_ = cl.c.send(context.Background(), reply)
		}
	}
	cl.mu.Lock()
	for id, ch := range cl.pending {
		close(ch)
		delete(cl.pending, id)
	}
	cl.mu.Unlock()
}

// call sends a request and waits for its answer. An error answer is a *rpcError.
func (cl *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := json.RawMessage(strconv.FormatInt(cl.next.Add(1), 10))
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	msg, _ := json.Marshal(message{JSONRPC: "2.0", ID: id, Method: method, Params: raw})
	ch := make(chan message, 1)
	cl.mu.Lock()
	select {
	case <-cl.done:
		cl.mu.Unlock()
		return nil, errClosed
	default:
	}
	cl.pending[string(id)] = ch
	cl.mu.Unlock()
	if err := cl.c.send(ctx, msg); err != nil {
		cl.mu.Lock()
		delete(cl.pending, string(id))
		cl.mu.Unlock()
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, errClosed
		}
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	case <-ctx.Done():
		cl.mu.Lock()
		delete(cl.pending, string(id))
		cl.mu.Unlock()
		cancel, _ := json.Marshal(map[string]any{"requestId": id, "reason": "cancelled"})
		note, _ := json.Marshal(message{JSONRPC: "2.0", Method: "notifications/cancelled", Params: cancel})
		_ = cl.c.send(context.Background(), note)
		return nil, ctx.Err()
	}
}

func (cl *client) notify(ctx context.Context, method string) error {
	msg, _ := json.Marshal(message{JSONRPC: "2.0", Method: method})
	return cl.c.send(ctx, msg)
}

// initialize opens the session.
func (cl *client) initialize(ctx context.Context, version string) error {
	_, err := cl.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": brand.CLILong, "version": version},
	})
	if err != nil {
		return fmt.Errorf("initializing: %w", err)
	}
	return cl.notify(ctx, "notifications/initialized")
}

// listTools returns every tool the server offers, each as the server described it.
func (cl *client) listTools(ctx context.Context) ([]map[string]any, error) {
	var out []map[string]any
	cursor := ""
	for range 100 {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := cl.call(ctx, "tools/list", params)
		if err != nil {
			return nil, fmt.Errorf("listing the tools: %w", err)
		}
		var page struct {
			Tools      []map[string]any `json:"tools"`
			NextCursor string           `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("listing the tools: %w", err)
		}
		out = append(out, page.Tools...)
		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
	return out, errors.New("listing the tools: more than 100 pages")
}

// callTool calls one tool and returns its result as the server wrote it.
func (cl *client) callTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	return cl.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
}

func (cl *client) close() error {
	err := cl.c.close()
	<-cl.done
	return err
}
