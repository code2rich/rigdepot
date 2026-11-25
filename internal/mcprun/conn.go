// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package mcprun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// conn is a connection to an upstream MCP server: messages go out with send and come in on
// recv, which is closed when the connection ends (err says why).
type conn interface {
	send(ctx context.Context, msg []byte) error
	recv() <-chan []byte
	err() error
	close() error
}

// connect opens a connection to s with the secrets filled in. stderr receives what a stdio
// server writes to its standard error.
func connect(ctx context.Context, s Server, secrets map[string][]byte, stderr io.Writer, client *http.Client) (conn, error) {
	switch s.Transport {
	case Stdio:
		return startProcess(s, secrets, stderr)
	case HTTP:
		return newHTTPConn(s.URL, resolve(s.Headers, secrets), client), nil
	case SSE:
		return newSSEConn(ctx, s.URL, resolve(s.Headers, secrets), client)
	}
	return nil, fmt.Errorf("unknown transport %q", s.Transport)
}

// environ is the process environment with the server's variables on top.
func environ(s Server, secrets map[string][]byte) []string {
	env := os.Environ()
	vars := resolve(s.Env, secrets)
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		env = slices.DeleteFunc(env, func(e string) bool { return strings.HasPrefix(e, k+"=") })
		env = append(env, k+"="+vars[k])
	}
	return env
}

// processConn is a stdio server: a child process spoken to over its standard input and output.
type processConn struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	w     *lineWriter
	ch    chan []byte
	done  chan struct{}
	mu    sync.Mutex
	cause error
}

func startProcess(s Server, secrets map[string][]byte, stderr io.Writer) (*processConn, error) {
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Env = environ(s, secrets)
	cmd.Stderr = stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", s.Command, err)
	}
	c := &processConn{cmd: cmd, in: in, w: &lineWriter{w: in}, ch: make(chan []byte, 16), done: make(chan struct{})}
	go func() {
		err := readLines(out, func(line []byte) { c.ch <- slices.Clone(line) })
		werr := cmd.Wait()
		c.mu.Lock()
		c.cause = errors.Join(err, werr)
		if c.cause == nil {
			c.cause = io.EOF
		}
		c.mu.Unlock()
		close(c.ch)
		close(c.done)
	}()
	return c, nil
}

func (c *processConn) send(_ context.Context, msg []byte) error { return c.w.write(msg) }
func (c *processConn) recv() <-chan []byte                      { return c.ch }

func (c *processConn) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cause
}

// close ends the process the way MCP's stdio transport says: close its input, wait, then stop
// it.
func (c *processConn) close() error {
	c.in.Close()
	select {
	case <-c.done:
		return nil
	case <-time.After(2 * time.Second):
	}
	c.cmd.Process.Kill()
	<-c.done
	return nil
}

// httpConn is a server over MCP's streamable HTTP transport: every message is a POST; the answer
// is JSON, or a stream of server-sent events, or nothing (202). The session the server gives at
// initialization goes with every later request.
type httpConn struct {
	url     string
	headers map[string]string
	client  *http.Client
	ch      chan []byte

	gate      sync.Mutex // held while a message is being started
	mu        sync.Mutex
	session   string
	version   string
	initID    string
	closed    bool
	inflight  sync.WaitGroup
	closeOnce sync.Once
}

func newHTTPConn(u string, headers map[string]string, client *http.Client) *httpConn {
	return &httpConn{url: u, headers: headers, client: client, ch: make(chan []byte, 16)}
}

func (c *httpConn) recv() <-chan []byte { return c.ch }
func (c *httpConn) err() error          { return io.EOF }

func (c *httpConn) deliver(msg []byte) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	var m message
	if json.Unmarshal(msg, &m) == nil && m.isResponse() && string(m.ID) == c.initIDValue() && len(m.Result) > 0 {
		var r struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(m.Result, &r) == nil {
			c.mu.Lock()
			c.version = r.ProtocolVersion
			c.mu.Unlock()
		}
	}
	c.ch <- msg
}

func (c *httpConn) initIDValue() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.initID
}

// send posts one message. The handshake, notifications and answers to the server are sent one
// after the other and each is done before the next message goes out, so that the server sees
// them in order and every later request carries the session; requests after the handshake run
// side by side, as a tool call can take long.
func (c *httpConn) send(ctx context.Context, msg []byte) error {
	var m message
	if err := json.Unmarshal(msg, &m); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(context.WithoutCancel(ctx), http.MethodPost, c.url, bytes.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	c.gate.Lock()
	c.setHeaders(req)
	c.inflight.Add(1)
	if m.Method == "initialize" || !m.isRequest() {
		defer c.gate.Unlock()
		if m.Method == "initialize" {
			c.mu.Lock()
			c.initID = string(m.ID)
			c.mu.Unlock()
		}
		c.exchange(req, m)
		c.inflight.Done()
		return nil
	}
	c.gate.Unlock()
	go func() {
		defer c.inflight.Done()
		c.exchange(req, m)
	}()
	return nil
}

func (c *httpConn) setHeaders(req *http.Request) {
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	if c.version != "" {
		req.Header.Set("MCP-Protocol-Version", c.version)
	}
}

// exchange sends one message and delivers what comes back. A failure answers the request, if it
// was one, with an error, so that the other side is not left waiting.
func (c *httpConn) exchange(req *http.Request, m message) {
	fail := func(text string) {
		if m.isRequest() {
			c.deliver(errorResponse(m.ID, codeServer, text))
		}
	}
	resp, err := c.client.Do(req)
	if err != nil {
		fail("the MCP server cannot be reached: " + err.Error())
		return
	}
	defer resp.Body.Close()
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		c.mu.Lock()
		c.session = s
		c.mu.Unlock()
	}
	switch {
	case resp.StatusCode == http.StatusAccepted:
		return
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		fail(fmt.Sprintf("the MCP server answered %s: %s", resp.Status, strings.TrimSpace(string(body))))
		return
	}
	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		readEvents(resp.Body, func(event, data string) {
			if event == "" || event == "message" {
				c.deliver([]byte(data))
			}
		})
	default:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxMessage))
		if err != nil {
			fail("reading the MCP server's answer: " + err.Error())
			return
		}
		body = bytes.TrimSpace(body)
		if len(body) == 0 {
			fail("the MCP server answered without a message")
			return
		}
		if body[0] == '[' {
			var batch []json.RawMessage
			if json.Unmarshal(body, &batch) == nil {
				for _, b := range batch {
					c.deliver(b)
				}
				return
			}
		}
		c.deliver(body)
	}
}

func (c *httpConn) close() error {
	c.closeOnce.Do(func() {
		c.inflight.Wait()
		c.mu.Lock()
		session := c.session
		c.closed = true
		c.mu.Unlock()
		if session != "" {
			// Ending the session is a courtesy: the server ends idle ones anyway.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url, nil); err == nil {
				c.setHeaders(req)
				if resp, err := c.client.Do(req); err == nil {
					resp.Body.Close()
				}
			}
		}
		close(c.ch)
	})
	return nil
}

// readEvents reads server-sent events and calls fn with the type and data of each.
func readEvents(r io.Reader, fn func(event, data string)) {
	br := bufio.NewReaderSize(r, 64<<10)
	var event string
	var data []string
	for {
		line, err := readLine(br)
		s := string(line)
		switch {
		case s == "":
			if len(data) > 0 {
				fn(event, strings.Join(data, "\n"))
			}
			event, data = "", nil
		case strings.HasPrefix(s, ":"):
		default:
			field, value, _ := strings.Cut(s, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				event = value
			case "data":
				data = append(data, value)
			}
		}
		if err != nil {
			if len(data) > 0 {
				fn(event, strings.Join(data, "\n"))
			}
			return
		}
	}
}

// sseConn is a server over MCP's older HTTP transport: a GET opens a stream of server-sent
// events, whose first event names where to POST the messages; the answers come on the stream.
type sseConn struct {
	headers  map[string]string
	client   *http.Client
	endpoint string
	ch       chan []byte
	body     io.Closer
	cancel   context.CancelFunc
	done     chan struct{}
}

func newSSEConn(ctx context.Context, u string, headers map[string]string, client *http.Client) (*sseConn, error) {
	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	req, err := http.NewRequestWithContext(sctx, http.MethodGet, u, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("the MCP server cannot be reached: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("the MCP server answered %s", resp.Status)
	}
	c := &sseConn{headers: headers, client: client, ch: make(chan []byte, 16), body: resp.Body, cancel: cancel, done: make(chan struct{})}
	endpoint := make(chan string, 1)
	go func() {
		defer close(c.done)
		defer close(c.ch)
		readEvents(resp.Body, func(event, data string) {
			switch event {
			case "endpoint":
				select {
				case endpoint <- data:
				default:
				}
			case "", "message":
				c.ch <- []byte(data)
			}
		})
	}()
	select {
	case e := <-endpoint:
		base, _ := url.Parse(u)
		ref, err := url.Parse(e)
		if err != nil {
			c.close()
			return nil, fmt.Errorf("the MCP server named an endpoint that is not an address: %q", e)
		}
		abs := base.ResolveReference(ref)
		if abs.Scheme != base.Scheme || abs.Host != base.Host {
			// The secrets go to the server they were meant for and nowhere else.
			c.close()
			return nil, fmt.Errorf("the MCP server sent its messages to another host (%s)", abs.Host)
		}
		c.endpoint = abs.String()
	case <-c.done:
		return nil, errors.New("the MCP server closed the stream before naming its endpoint")
	case <-time.After(30 * time.Second):
		c.close()
		return nil, errors.New("the MCP server did not name its endpoint within 30 seconds")
	}
	return c, nil
}

func (c *sseConn) recv() <-chan []byte { return c.ch }
func (c *sseConn) err() error          { return io.EOF }

func (c *sseConn) send(ctx context.Context, msg []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(msg))
	if err != nil {
		return err
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the MCP server answered %s", resp.Status)
	}
	return nil
}

func (c *sseConn) close() error {
	c.cancel()
	c.body.Close()
	<-c.done
	return nil
}
