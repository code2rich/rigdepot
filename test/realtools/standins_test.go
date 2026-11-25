// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package realtools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The MCP server stand-ins answer just enough of the protocol for a tool's health check:
// initialize, the initialized notification, ping, and empty lists of tools, prompts and
// resources.

type rpcRequest struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params struct {
		ProtocolVersion string `json:"protocolVersion"`
	} `json:"params"`
}

// answer returns the reply to one JSON-RPC message, or nil for a notification.
func answer(raw []byte) []byte {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`)
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return nil
	}
	var result any
	switch req.Method {
	case "initialize":
		version := req.Params.ProtocolVersion
		if version == "" {
			version = "2025-03-26"
		}
		result = map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "realtools-stand-in", "version": "1.0.0"},
		}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": []any{}}
	case "prompts/list":
		result = map[string]any{"prompts": []any{}}
	case "resources/list":
		result = map[string]any{"resources": []any{}}
	case "resources/templates/list":
		result = map[string]any{"resourceTemplates": []any{}}
	default:
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
		return b
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	return b
}

// serveStdio is the stdio stand-in: the test binary started by a tool, with fakeMCPVariable
// naming the file it notes its start in. It answers until standard input ends.
func serveStdio(out string) int {
	if f, err := os.OpenFile(out, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600); err == nil {
		fmt.Fprintf(f, "started args=%q RT_MODE=%s\n", os.Args[1:], os.Getenv("RT_MODE"))
		f.Close()
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		if reply := answer([]byte(line)); reply != nil {
			os.Stdout.Write(append(reply, '\n'))
		}
	}
	return 0
}

// httpStandIn is the HTTP stand-in on the loopback address. It records the Authorization header
// of every request.
type httpStandIn struct {
	url  string
	mu   sync.Mutex
	auth []string
}

func startHTTPStandIn(t *testing.T) *httpStandIn {
	h := &httpStandIn{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.auth = append(h.auth, r.Header.Get("Authorization"))
		h.mu.Unlock()
		if r.Method != http.MethodPost {
			// No server-sent event stream and no sessions to end.
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reply := answer(body)
		if reply == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(reply)
	}))
	t.Cleanup(srv.Close)
	h.url = srv.URL + "/mcp"
	return h
}

func (h *httpStandIn) reset() {
	h.mu.Lock()
	h.auth = nil
	h.mu.Unlock()
}

func (h *httpStandIn) authorizations() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.auth...)
}

// opencodeServer is opencode serve, started inside the temporary home.
type opencodeServer struct {
	url, password string
	cmd           *exec.Cmd
	client        *http.Client
}

func startOpencode(t *testing.T, s *suite) *opencodeServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	cmd := exec.Command(realTools["opencode"], "serve", "--hostname", "127.0.0.1", "--port", fmt.Sprint(port))
	cmd.Dir = filepath.Join(s.synced, "work")
	cmd.Env = s.env(s.synced)
	ownProcessGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	srv := &opencodeServer{cmd: cmd, client: &http.Client{Timeout: 10 * time.Second}}

	// It prints where it listens and the password it made up for this run.
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	deadline := time.After(90 * time.Second)
	var seen []string
	for srv.url == "" || srv.password == "" {
		select {
		case line, ok := <-lines:
			if !ok {
				srv.stop()
				t.Fatalf("opencode serve ended before it was ready:\n%s", strings.Join(seen, "\n"))
			}
			seen = append(seen, line)
			if v, ok := strings.CutPrefix(line, "server listening on "); ok {
				srv.url = strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(line, "server password "); ok {
				srv.password = strings.TrimSpace(v)
			}
		case <-deadline:
			srv.stop()
			t.Fatalf("opencode serve did not say where it listens within 90s:\n%s", strings.Join(seen, "\n"))
		}
	}
	go func() {
		for range lines {
		}
	}()
	return srv
}

func (o *opencodeServer) stop() {
	killProcessGroup(o.cmd)
	_ = o.cmd.Wait()
}

func (o *opencodeServer) get(path string, v any) error {
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(o.url, "/")+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth("opencode", o.password)
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s: %s", path, resp.Status, body)
	}
	return json.Unmarshal(body, v)
}

// waitForMCP waits until the server has tried each named MCP server and returns each one's status.
// A server that was just started has not tried any and lists none.
func (o *opencodeServer) waitForMCP(t *testing.T, names []string) map[string]string {
	t.Helper()
	settled := func(st string) bool { return st != "" && st != "connecting" && st != "pending" }
	var last map[string]string
	for start := time.Now(); time.Since(start) < 90*time.Second; time.Sleep(time.Second) {
		var doc struct {
			Data []struct {
				Name   string `json:"name"`
				Status struct {
					Status string `json:"status"`
					Error  string `json:"error"`
				} `json:"status"`
			} `json:"data"`
		}
		if err := o.get("/api/mcp", &doc); err != nil {
			t.Logf("waiting for opencode serve: %v", err)
			continue
		}
		last = map[string]string{}
		for _, d := range doc.Data {
			last[d.Name] = d.Status.Status
			if d.Status.Error != "" {
				last[d.Name] += ": " + d.Status.Error
			}
		}
		done := true
		for _, n := range names {
			if !settled(last[n]) {
				done = false
			}
		}
		if done {
			return last
		}
	}
	t.Fatalf("opencode serve did not settle the MCP servers within 90s; last: %v", last)
	return nil
}

// skills returns the identifiers in the server's skill list.
func (o *opencodeServer) skills(t *testing.T) []string {
	t.Helper()
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := o.get("/api/skill", &doc); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range doc.Data {
		ids = append(ids, d.ID)
	}
	return ids
}
