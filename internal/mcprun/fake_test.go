// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package mcprun

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/code2rich/rigdepot/internal/testenv"
)

// fakeEnv makes the test binary a fake stdio MCP server (fakeStdioServer) instead of running
// the tests: the servers the tests start are this program.
const fakeEnv = "MCPRUN_FAKE_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) == "1" {
		fakeStdioServer()
		os.Exit(0)
	}
	testenv.Main(m)
}

// fakeTools are the tools both fake servers offer.
var fakeTools = []map[string]any{
	{"name": "env", "description": "Returns the value of an environment variable.", "inputSchema": map[string]any{"type": "object"}},
	{"name": "echo", "description": "Returns its arguments.", "inputSchema": map[string]any{"type": "object"}},
	{"name": "fail", "description": "Always fails.", "inputSchema": map[string]any{"type": "object"}},
	{"name": "dot.name", "description": "A name with a dot.", "inputSchema": map[string]any{"type": "object"}},
}

// fakeAnswer is how both fake servers answer a request; header is the value of the request
// header the HTTP one received (the env tool of the stdio one reads the environment instead).
func fakeAnswer(m message, lookup func(string) string) []byte {
	switch m.Method {
	case "initialize":
		return response(m.ID, map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "fake", "version": "1.0.0"}})
	case "tools/list":
		return response(m.ID, map[string]any{"tools": fakeTools})
	case "tools/call":
		var p struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		json.Unmarshal(m.Params, &p)
		text := func(s string, isError bool) []byte {
			return response(m.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": s}}, "isError": isError})
		}
		switch p.Name {
		case "env":
			return text(lookup(p.Arguments["name"]), false)
		case "echo":
			raw, _ := json.Marshal(p.Arguments)
			return text(string(raw), false)
		case "fail":
			return text("it failed", true)
		case "dot.name":
			return text("dot", false)
		}
		return errorResponse(m.ID, codeInvalidParams, "unknown tool "+p.Name)
	case "ping":
		return response(m.ID, struct{}{})
	}
	return errorResponse(m.ID, codeMethodNotFound, "unknown method")
}

// fakeStdioServer speaks MCP over standard input and output until its input ends.
func fakeStdioServer() {
	out := bufio.NewWriter(os.Stdout)
	readLines(os.Stdin, func(line []byte) {
		var m message
		if json.Unmarshal(line, &m) != nil || !m.isRequest() {
			return
		}
		out.Write(fakeAnswer(m, os.Getenv))
		out.WriteString("\n")
		out.Flush()
	})
}

// fakeHTTP is a fake MCP server over streamable HTTP. It answers in JSON, or as a stream of
// server-sent events when sse is set, and records the headers it got.
type fakeHTTP struct {
	srv *httptest.Server
	sse bool

	mu       sync.Mutex
	auth     []string // Authorization of every POST
	sessions []string // Mcp-Session-Id of every POST after the first
	versions []string // MCP-Protocol-Version of every POST after the first
	deleted  bool
}

func newFakeHTTP(t *testing.T, sse bool) *fakeHTTP {
	f := &fakeHTTP{sse: sse}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodDelete {
			f.deleted = r.Header.Get("Mcp-Session-Id") == "s-1"
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var m message
		json.NewDecoder(r.Body).Decode(&m)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if m.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "s-1")
		} else {
			f.sessions = append(f.sessions, r.Header.Get("Mcp-Session-Id"))
			f.versions = append(f.versions, r.Header.Get("MCP-Protocol-Version"))
		}
		if !m.isRequest() {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		answer := fakeAnswer(m, func(name string) string { return r.Header.Get(name) })
		if f.sse {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, ": comment\nevent: message\ndata: %s\n\n", answer)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(answer)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// fakeLegacySSE is a fake MCP server over the older HTTP transport with server-sent events.
type fakeLegacySSE struct {
	srv  *httptest.Server
	mu   sync.Mutex
	auth []string
	out  chan []byte
}

func newFakeLegacySSE(t *testing.T) *fakeLegacySSE {
	f := &fakeLegacySSE{out: make(chan []byte, 16)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sse":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: endpoint\ndata: /messages?session=1\n\n")
			w.(http.Flusher).Flush()
			for {
				select {
				case msg := <-f.out:
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		case r.Method == http.MethodPost && r.URL.Path == "/messages":
			var m message
			json.NewDecoder(r.Body).Decode(&m)
			w.WriteHeader(http.StatusAccepted)
			if m.isRequest() {
				auth := r.Header.Get("Authorization")
				f.out <- fakeAnswer(m, func(string) string { return auth })
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// fakeStdio is a stdio server that is this test binary.
func fakeStdio(code string, env map[string]Value) Server {
	if env == nil {
		env = map[string]Value{}
	}
	env[fakeEnv] = Value{Literal: "1"}
	return Server{Schema: 1, Code: code, Name: strings.ToUpper(code[:1]) + code[1:], Transport: Stdio, Command: os.Args[0], Env: env}
}
