// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package mcprun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
)

// StartTimeout bounds how long Serve and Call wait for one server to start and list its tools.
const StartTimeout = 30 * time.Second

// upstream is one server behind the single entry.
type upstream struct {
	s     Server
	cl    *client
	tools []map[string]any // as offered: renamed and described
	// unavailable is why the server cannot be used, for the AI; its tools are stand-ins then.
	unavailable string
}

// proxy is the single entry: every native server behind one MCP server.
type proxy struct {
	o     Options
	out   *lineWriter
	start sync.Once
	ready chan struct{}
	ups   []*upstream
	// route is the offered name of every tool → its server and its own name.
	route map[string]routed

	mu       sync.Mutex
	inflight map[string]context.CancelFunc // by request id
	wg       sync.WaitGroup
}

type routed struct {
	up   *upstream
	name string
}

// Serve is the single entry (rgd mcp serve): one MCP server on o's streams that offers the tools
// of every server of servers not delivered on demand, each named ToolName(code, tool), and
// passes every call on to its server. The servers are started when the AI tool initializes the
// session, all at once; one that cannot start (a missing secret above all) offers stand-in
// tools that answer with why. Serve returns when the tool closes its input.
func Serve(ctx context.Context, servers []Server, o Options) error {
	o = o.shared()
	p := &proxy{o: o, out: &lineWriter{w: o.Stdout}, ready: make(chan struct{}), route: map[string]routed{}, inflight: map[string]context.CancelFunc{}}
	for _, s := range servers {
		if !s.OnDemand {
			p.ups = append(p.ups, &upstream{s: s})
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	err := readLines(o.Stdin, func(line []byte) { p.handle(ctx, line) })
	p.wg.Wait()
	p.start.Do(func() { close(p.ready) }) // nothing was started
	<-p.ready
	for _, u := range p.ups {
		if u.cl != nil {
			u.cl.close()
		}
	}
	return err
}

func (p *proxy) handle(ctx context.Context, line []byte) {
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		p.out.write(errorResponse(nil, codeParseError, "not JSON"))
		return
	}
	switch {
	case m.isNotification():
		if m.Method == "notifications/cancelled" {
			var c struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			if json.Unmarshal(m.Params, &c) == nil {
				p.mu.Lock()
				if cancel := p.inflight[string(c.RequestID)]; cancel != nil {
					cancel()
				}
				p.mu.Unlock()
			}
		}
		return
	case !m.isRequest():
		return
	}
	switch m.Method {
	case "initialize":
		p.startAll(ctx)
		p.out.write(response(m.ID, initializeResult(m.Params, brand.MCPEntry, p.o.Version, "")))
	case "ping":
		p.out.write(response(m.ID, struct{}{}))
	case "tools/list", "tools/call":
		rctx, cancel := context.WithCancel(ctx)
		p.mu.Lock()
		p.inflight[string(m.ID)] = cancel
		p.mu.Unlock()
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer func() {
				p.mu.Lock()
				delete(p.inflight, string(m.ID))
				p.mu.Unlock()
				cancel()
			}()
			p.startAll(ctx)
			select {
			case <-p.ready:
			case <-rctx.Done():
				return
			}
			if m.Method == "tools/list" {
				p.out.write(response(m.ID, map[string]any{"tools": p.tools()}))
				return
			}
			p.out.write(p.call(rctx, m))
		}()
	default:
		p.out.write(errorResponse(m.ID, codeMethodNotFound, "not supported by the single entry"))
	}
}

// startAll starts every server once, in parallel, and closes ready when all are up or given up.
func (p *proxy) startAll(ctx context.Context) {
	p.start.Do(func() {
		go func() {
			var wg sync.WaitGroup
			for _, u := range p.ups {
				wg.Add(1)
				go func() {
					defer wg.Done()
					p.startOne(ctx, u)
				}()
			}
			wg.Wait()
			p.names()
			close(p.ready)
		}()
	})
}

func (p *proxy) startOne(ctx context.Context, u *upstream) {
	fail := func(err error) {
		u.unavailable = p.o.explain(err)
		fmt.Fprintln(p.o.Stderr, u.s.Code+": "+u.unavailable)
	}
	secrets, err := p.o.secretsFor(u.s)
	if err != nil {
		fail(err)
		return
	}
	defer wipe(secrets)
	sctx, cancel := context.WithTimeout(ctx, StartTimeout)
	defer cancel()
	c, err := connect(sctx, u.s, secrets, p.o.Stderr, p.o.http())
	if err != nil {
		fail(err)
		return
	}
	cl := newClient(c)
	if err := cl.initialize(sctx, p.o.Version); err != nil {
		cl.close()
		fail(fmt.Errorf("the MCP server %s did not start: %w", u.s.Code, err))
		return
	}
	tools, err := cl.listTools(sctx)
	if err != nil {
		cl.close()
		fail(fmt.Errorf("the MCP server %s did not list its tools: %w", u.s.Code, err))
		return
	}
	u.cl, u.tools = cl, tools
}

// names gives every tool its offered name, once all servers are up. Two tools that would get
// the same name (after the replacement of characters, or after cutting) are told apart by the
// digest of their full names.
func (p *proxy) names() {
	for _, u := range p.ups {
		if u.cl == nil {
			u.tools = standInTools(u.s, func(t string) string { return ToolName(u.s.Code, t) }, u.unavailable)
			for _, t := range u.tools {
				p.route[t["name"].(string)] = routed{up: u}
			}
			continue
		}
		offered := make([]map[string]any, 0, len(u.tools))
		for _, t := range u.tools {
			orig, _ := t["name"].(string)
			if orig == "" {
				continue
			}
			name := ToolName(u.s.Code, orig)
			if _, taken := p.route[name]; taken {
				sum := sha256.Sum256([]byte(u.s.Code + Separator + orig))
				suffix := "_" + hex.EncodeToString(sum[:])[:hashLen]
				name = name[:min(len(name), MaxToolName-len(suffix))] + suffix
			}
			if _, taken := p.route[name]; taken {
				fmt.Fprintf(p.o.Stderr, "%s: the tool %q is left out: its name is taken\n", u.s.Code, orig)
				continue
			}
			p.route[name] = routed{up: u, name: orig}
			c := make(map[string]any, len(t))
			for k, v := range t {
				c[k] = v
			}
			c["name"] = name
			if d, _ := t["description"].(string); u.s.Name != "" {
				c["description"] = u.s.Name + ": " + d
			}
			offered = append(offered, c)
		}
		u.tools = offered
	}
}

func (p *proxy) tools() []map[string]any {
	out := []map[string]any{}
	for _, u := range p.ups {
		out = append(out, u.tools...)
	}
	return out
}

// call passes a tools/call on to the server of the tool.
func (p *proxy) call(ctx context.Context, m message) []byte {
	var params map[string]json.RawMessage
	if err := json.Unmarshal(m.Params, &params); err != nil {
		return errorResponse(m.ID, codeInvalidParams, "the parameters are not an object")
	}
	var name string
	json.Unmarshal(params["name"], &name)
	r, ok := p.route[name]
	switch {
	case !ok:
		return errorResponse(m.ID, codeInvalidParams, fmt.Sprintf("unknown tool %q", name))
	case r.up.cl == nil:
		return response(m.ID, toolError(r.up.unavailable))
	}
	fwd := map[string]any{"name": r.name, "arguments": orEmpty(params["arguments"])}
	if meta := params["_meta"]; len(meta) > 0 {
		fwd["_meta"] = meta
	}
	raw, err := r.up.cl.call(ctx, "tools/call", fwd)
	var rerr *rpcError
	switch {
	case errors.As(err, &rerr):
		return errorResponse(m.ID, rerr.Code, rerr.Message)
	case errors.Is(err, context.Canceled):
		return errorResponse(m.ID, codeServer, "cancelled")
	case err != nil:
		return response(m.ID, toolError(fmt.Sprintf("the MCP server %s: %v", r.up.s.Code, err)))
	}
	data, _ := json.Marshal(message{JSONRPC: "2.0", ID: m.ID, Result: raw})
	return data
}

func orEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}
