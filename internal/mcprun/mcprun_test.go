// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package mcprun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/home"
)

// marker is the value of the personal secret in the tests.
const marker = "Hw4-mcprun-secret-marker-6Tq1-not-a-token"

// secretsOf gives the secrets in vals; asking for any other is a *MissingSecretsError.
func secretsOf(vals map[string]string) SecretSource {
	return func(names []string) (map[string][]byte, error) {
		out := map[string][]byte{}
		var missing []string
		for _, n := range names {
			v, ok := vals[n]
			if !ok {
				missing = append(missing, n)
				continue
			}
			out[n] = []byte(v)
		}
		if len(missing) > 0 {
			return nil, &MissingSecretsError{Names: missing}
		}
		return out, nil
	}
}

// session is what an AI tool sends: the handshake, then the requests.
func session(requests ...string) string {
	lines := []string{
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	}
	return strings.Join(append(lines, requests...), "\n") + "\n"
}

func callLine(id int, tool, args string) string {
	return `{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}`
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// answers reads the responses an AI tool got, by id.
func answers(t *testing.T, out string) map[string]message {
	t.Helper()
	got := map[string]message{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var m message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not a message: %q", line)
		}
		got[string(m.ID)] = m
	}
	return got
}

// text returns the text of a tool result, and whether it is an error.
func text(t *testing.T, m message) (string, bool) {
	t.Helper()
	if m.Error != nil {
		t.Fatalf("error answer: %+v", m.Error)
	}
	var r struct {
		Content []struct{ Text string } `json:"content"`
		IsError bool                    `json:"isError"`
	}
	if err := json.Unmarshal(m.Result, &r); err != nil || len(r.Content) == 0 {
		t.Fatalf("not a tool result: %s", m.Result)
	}
	return r.Content[0].Text, r.IsError
}

func opts(in string, secrets SecretSource) (Options, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return Options{Secrets: secrets, Stdin: strings.NewReader(in), Stdout: &out, Stderr: &errOut, Version: "0.0.0-test"}, &out, &errOut
}

// rgd mcp run on a stdio server: the secret is in the server's environment, under the name the
// resource gives it, and the literal ones too.
func TestRunStdioInjectsTheEnvironment(t *testing.T) {
	s := fakeStdio("jira", map[string]Value{"JIRA_TOKEN": {Secret: "JIRA_API_TOKEN"}, "AUTH": {Literal: "Bearer ", Secret: "JIRA_API_TOKEN"}, "JIRA_URL": {Literal: "https://jira.example.com"}})
	o, out, errOut := opts(session(
		callLine(1, "env", `{"name":"JIRA_TOKEN"}`),
		callLine(2, "env", `{"name":"AUTH"}`),
		callLine(3, "env", `{"name":"JIRA_URL"}`),
	), secretsOf(map[string]string{"JIRA_API_TOKEN": marker}))
	if err := Run(context.Background(), s, o); err != nil {
		t.Fatalf("run: %v\n%s", err, errOut)
	}
	got := answers(t, out.String())
	for id, want := range map[string]string{"1": marker, "2": "Bearer " + marker, "3": "https://jira.example.com"} {
		if v, _ := text(t, got[id]); v != want {
			t.Errorf("call %s: %q, want %q", id, v, want)
		}
	}
}

// A missing secret is not a server that fails to start: the AI tool gets a server whose tools
// say what is missing and how to set it, and the server itself is never started.
func TestRunMissingSecretAnswersWithTheReason(t *testing.T) {
	s := Server{Schema: 1, Code: "jira", Transport: Stdio, Command: filepath.Join(t.TempDir(), "not-there"),
		Env: map[string]Value{"JIRA_TOKEN": {Secret: "JIRA_API_TOKEN"}}, Tools: []Tool{{Name: "search_issues"}}}
	o, out, errOut := opts(session(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		callLine(2, "search_issues", `{}`),
	), secretsOf(nil))
	if err := Run(context.Background(), s, o); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := answers(t, out.String())
	if got["0"].Error != nil || !strings.Contains(string(got["0"].Result), "JIRA_API_TOKEN") {
		t.Errorf("initialize: %s", got["0"].Result)
	}
	if !strings.Contains(string(got["1"].Result), `"search_issues"`) {
		t.Errorf("tools: %s", got["1"].Result)
	}
	msg, isErr := text(t, got["2"])
	if !isErr || !strings.Contains(msg, "JIRA_API_TOKEN") || !strings.Contains(msg, "secret set JIRA_API_TOKEN") {
		t.Errorf("call: %q", msg)
	}
	if !strings.Contains(errOut.String(), "JIRA_API_TOKEN") {
		t.Errorf("stderr: %q", errOut)
	}

	// A keychain that cannot be read is said too, and is not a missing secret.
	o, out, _ = opts(session(callLine(1, "search_issues", `{}`)), func([]string) (map[string][]byte, error) {
		return nil, errors.New("the keychain is locked")
	})
	Run(context.Background(), s, o)
	if msg, isErr := text(t, answers(t, out.String())["1"]); !isErr || !strings.Contains(msg, "keychain is locked") {
		t.Errorf("keychain: %q", msg)
	}
}

// rgd mcp run on an HTTP server bridges standard input and output to it, with the secret in
// the request header and the session the server gave in every later request; answers in JSON
// and as event streams both reach the AI tool.
func TestRunBridgesHTTP(t *testing.T) {
	for _, sse := range []bool{false, true} {
		f := newFakeHTTP(t, sse)
		s := Server{Schema: 1, Code: "jira", Transport: HTTP, URL: f.srv.URL + "/mcp",
			Headers: map[string]Value{"Authorization": {Literal: "Bearer ", Secret: "JIRA_API_TOKEN"}}}
		o, out, errOut := opts(session(
			callLine(1, "env", `{"name":"Authorization"}`),
			callLine(2, "echo", `{"a":"b"}`),
		), secretsOf(map[string]string{"JIRA_API_TOKEN": marker}))
		if err := Run(context.Background(), s, o); err != nil {
			t.Fatalf("sse=%v: %v\n%s", sse, err, errOut)
		}
		got := answers(t, out.String())
		if v, _ := text(t, got["1"]); v != "Bearer "+marker {
			t.Errorf("sse=%v: header %q", sse, v)
		}
		if v, _ := text(t, got["2"]); v != `{"a":"b"}` {
			t.Errorf("sse=%v: echo %q", sse, v)
		}
		f.mu.Lock()
		for _, a := range f.auth {
			if a != "Bearer "+marker {
				t.Errorf("sse=%v: a request without the secret: %q", sse, a)
			}
		}
		if len(f.sessions) != 3 || slices.ContainsFunc(f.sessions, func(s string) bool { return s != "s-1" }) ||
			slices.ContainsFunc(f.versions, func(s string) bool { return s != ProtocolVersion }) {
			t.Errorf("sse=%v: sessions %v versions %v", sse, f.sessions, f.versions)
		}
		if !f.deleted {
			t.Errorf("sse=%v: the session was not ended", sse)
		}
		f.mu.Unlock()
	}
}

// A server that cannot be reached answers every request with why.
func TestRunHTTPUnreachable(t *testing.T) {
	f := newFakeHTTP(t, false)
	f.srv.Close()
	s := Server{Schema: 1, Code: "jira", Transport: HTTP, URL: f.srv.URL}
	o, out, _ := opts(session(callLine(1, "env", `{}`)), nil)
	if err := Run(context.Background(), s, o); err != nil {
		t.Fatal(err)
	}
	got := answers(t, out.String())
	if got["0"].Error == nil || got["1"].Error == nil || !strings.Contains(got["1"].Error.Message, "cannot be reached") {
		t.Errorf("answers: %+v", got)
	}
}

func TestCall(t *testing.T) {
	s := fakeStdio("jira", map[string]Value{"JIRA_TOKEN": {Secret: "JIRA_API_TOKEN"}})
	o, _, _ := opts("", secretsOf(map[string]string{"JIRA_API_TOKEN": marker}))
	ctx := context.Background()
	raw, err := Call(ctx, s, "env", json.RawMessage(`{"name":"JIRA_TOKEN"}`), o)
	if err != nil || !strings.Contains(string(raw), marker) {
		t.Fatalf("call: %s %v", raw, err)
	}
	raw, err = Call(ctx, s, "", nil, o)
	if err != nil || !strings.Contains(string(raw), `"echo"`) || !strings.Contains(string(raw), `"inputSchema"`) {
		t.Errorf("list: %s %v", raw, err)
	}
	var te *ToolError
	if raw, err := Call(ctx, s, "fail", nil, o); !errors.As(err, &te) || !strings.Contains(string(raw), "it failed") {
		t.Errorf("failing tool: %s %v", raw, err)
	}
	var re *rpcError
	if _, err := Call(ctx, s, "nope", nil, o); !errors.As(err, &re) {
		t.Errorf("unknown tool: %v", err)
	}
	if _, err := Call(ctx, s, "echo", json.RawMessage(`[1]`), o); !errors.Is(err, ErrNotJSON) {
		t.Errorf("not an object: %v", err)
	}
	var missing *MissingSecretsError
	o.Secrets = secretsOf(nil)
	if _, err := Call(ctx, s, "env", nil, o); !errors.As(err, &missing) || missing.Server != "jira" || missing.Names[0] != "JIRA_API_TOKEN" {
		t.Errorf("missing: %v", err)
	}
	// Over the older HTTP transport too.
	f := newFakeLegacySSE(t)
	legacy := Server{Schema: 1, Code: "wiki", Transport: SSE, URL: f.srv.URL + "/sse", Headers: map[string]Value{"Authorization": {Secret: "JIRA_API_TOKEN"}}}
	o.Secrets = secretsOf(map[string]string{"JIRA_API_TOKEN": marker})
	if raw, err := Call(ctx, legacy, "env", nil, o); err != nil || !strings.Contains(string(raw), marker) {
		t.Errorf("sse: %s %v", raw, err)
	}
}

// rgd mcp serve: every native server behind one entry, its tools named ToolName(code, tool);
// calls reach the right server with its secret; a server without its secret offers stand-ins
// that say so; on-demand servers are not there.
func TestServe(t *testing.T) {
	f := newFakeHTTP(t, true)
	servers := []Server{
		fakeStdio("jira", map[string]Value{"JIRA_TOKEN": {Secret: "JIRA_API_TOKEN"}}),
		{Schema: 1, Code: "wiki", Name: "Wiki", Transport: HTTP, URL: f.srv.URL, Headers: map[string]Value{"Authorization": {Literal: "Bearer ", Secret: "WIKI_TOKEN"}}},
		{Schema: 1, Code: "figma", Name: "Figma", Transport: Stdio, Command: filepath.Join(t.TempDir(), "not-there"),
			Env: map[string]Value{"FIGMA_TOKEN": {Secret: "FIGMA_TOKEN"}}, Tools: []Tool{{Name: "get_file"}}},
		{Schema: 1, Code: "search", Transport: Stdio, Command: "x", OnDemand: true},
	}
	o, out, errOut := opts(session(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		callLine(2, "jira__env", `{"name":"JIRA_TOKEN"}`),
		callLine(3, "wiki__env", `{"name":"Authorization"}`),
		callLine(4, "figma__get_file", `{}`),
		callLine(5, "jira__dot_name", `{}`),
		callLine(6, "nope__x", `{}`),
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`,
	), secretsOf(map[string]string{"JIRA_API_TOKEN": marker, "WIKI_TOKEN": "w-" + marker}))
	done := make(chan error)
	go func() { done <- Serve(context.Background(), servers, o) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("serve did not end")
	}
	got := answers(t, out.String())
	var list struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	json.Unmarshal(got["1"].Result, &list)
	var names []string
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	want := []string{"figma__get_file", "jira__dot_name", "jira__echo", "jira__env", "jira__fail", "wiki__dot_name", "wiki__echo", "wiki__env", "wiki__fail"}
	if !slices.Equal(names, want) {
		t.Errorf("tools: %v\nwant %v", names, want)
	}
	if !strings.Contains(string(got["0"].Result), `"name":"rigdepot"`) {
		t.Errorf("initialize: %s", got["0"].Result)
	}
	if v, _ := text(t, got["2"]); v != marker {
		t.Errorf("jira: %q", v)
	}
	if v, _ := text(t, got["3"]); v != "Bearer w-"+marker {
		t.Errorf("wiki: %q", v)
	}
	if v, isErr := text(t, got["4"]); !isErr || !strings.Contains(v, "FIGMA_TOKEN") {
		t.Errorf("figma: %q", v)
	}
	if v, _ := text(t, got["5"]); v != "dot" {
		t.Errorf("renamed tool: %q", v)
	}
	if got["6"].Error == nil {
		t.Errorf("unknown tool: %+v", got["6"])
	}
	if got["7"].Error != nil {
		t.Errorf("ping: %+v", got["7"])
	}
	if !strings.Contains(errOut.String(), "figma") {
		t.Errorf("stderr: %q", errOut)
	}
}

func TestToolName(t *testing.T) {
	if got := ToolName("jira", "search_issues"); got != "jira__search_issues" {
		t.Errorf("plain: %s", got)
	}
	if got := ToolName("jira", "get.issue/v2"); got != "jira__get_issue_v2" {
		t.Errorf("sanitized: %s", got)
	}
	long := ToolName("unit-testing", strings.Repeat("a", 60))
	if len(long) != MaxToolName || !strings.HasPrefix(long, "unit-testing__aaa") || long != ToolName("unit-testing", strings.Repeat("a", 60)) {
		t.Errorf("long: %s (%d)", long, len(long))
	}
	if ToolName("unit-testing", strings.Repeat("a", 60)) == ToolName("unit-testing", strings.Repeat("a", 61)) {
		t.Error("two long names are the same")
	}
	exact := strings.Repeat("b", MaxToolName-len("jira__"))
	if got := ToolName("jira", exact); got != "jira__"+exact {
		t.Errorf("at the limit: %s", got)
	}
}

// connSpec is a resource kind's Spec that is an MCP server.
type connSpec struct{ c kind.Connection }

func (s connSpec) Connection() kind.Connection { return s.c }

func TestKeepAndLoad(t *testing.T) {
	data, err := home.At(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	st := &desired.State{Resources: []desired.Resource{
		{Ref: resource.Ref{Kind: "mcp", Code: "jira"}, Meta: resource.Meta{Name: "Jira"}, Digest: "sha256:1",
			Spec: connSpec{kind.Connection{Transport: "http", URL: "https://jira.example.com/mcp",
				Headers: map[string]adapter.Value{"Authorization": {Literal: "Bearer ", Secret: "JIRA_API_TOKEN"}},
				Tools:   []kind.ConnectionTool{{Name: "search_issues", Summary: "Search"}}}}},
		{Ref: resource.Ref{Kind: "skill", Code: "unit-testing"}},
	}}
	kept, err := Keep(data, st)
	if err != nil || !slices.Equal(kept.Written, []string{"jira"}) {
		t.Fatalf("keep: %+v %v", kept, err)
	}
	s, err := Load(data, "jira")
	if err != nil || s.Name != "Jira" || s.Headers["Authorization"].Secret != "JIRA_API_TOKEN" || len(s.Tools) != 1 || s.OnDemand {
		t.Fatalf("load: %+v %v", s, err)
	}
	if !slices.Equal(s.Secrets(), []string{"JIRA_API_TOKEN"}) {
		t.Errorf("secrets: %v", s.Secrets())
	}
	if _, err := Load(data, "wiki"); !errors.Is(err, ErrUnknownServer) {
		t.Errorf("unknown: %v", err)
	}
	// A server that could not be read stays; one no longer delivered goes.
	st.Failed = []desired.Failed{{Ref: resource.Ref{Kind: "mcp", Code: "jira"}}}
	st.Resources = st.Resources[1:]
	if kept, _ := Keep(data, st); len(kept.Removed) != 0 {
		t.Errorf("removed a server that could not be read: %+v", kept)
	}
	st.Failed = nil
	if kept, _ := Keep(data, st); !slices.Equal(kept.Removed, []string{"jira"}) {
		t.Errorf("not removed: %+v", kept)
	}
	if all, err := LoadAll(data); err != nil || len(all) != 0 {
		t.Errorf("all: %+v %v", all, err)
	}
}

// Every message the bridge writes is one line, also when several goroutines write.
func TestLineWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &lineWriter{w: &buf}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.write(response(json.RawMessage(itoa(i)), map[string]string{"x": strings.Repeat("y", 1000)}))
		}()
	}
	wg.Wait()
	if n := len(answers(t, buf.String())); n != 20 {
		t.Errorf("%d answers", n)
	}
}
