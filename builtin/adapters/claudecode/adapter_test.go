// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package claudecode

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/adapter/adaptertest"
	"github.com/code2rich/rigdepot/ext/resource"
)

func TestContract(t *testing.T) { adaptertest.Run(t, New(), "testdata") }

// fakeEnv is a computer with the given files (path -> content; nil content means the file
// exists but cannot be read) and environment variables.
func fakeEnv(files map[string][]byte, vars map[string]string) adapter.Env {
	return adapter.Env{
		GOOS:   "darwin",
		Home:   "/Users/example",
		Getenv: func(k string) string { return vars[k] },
		ReadFile: func(p string) ([]byte, error) {
			data, ok := files[p]
			switch {
			case !ok:
				return nil, fs.ErrNotExist
			case data == nil:
				return nil, fs.ErrPermission
			}
			return data, nil
		},
		Stat: func(p string) (fs.FileInfo, error) {
			data, ok := files[p]
			switch {
			case !ok:
				return nil, fs.ErrNotExist
			case data == nil:
				return nil, fs.ErrPermission
			}
			return fileInfo{}, nil
		},
		LookPath: func(string) (string, error) { return "/usr/local/bin/claude", nil },
		Version:  func(context.Context, string, ...string) (string, error) { return "2.1.330 (Claude Code)", nil },
	}
}

type fileInfo struct{}

func (fileInfo) Name() string       { return "f" }
func (fileInfo) Size() int64        { return 0 }
func (fileInfo) Mode() fs.FileMode  { return 0o600 }
func (fileInfo) ModTime() time.Time { return time.Time{} }
func (fileInfo) IsDir() bool        { return false }
func (fileInfo) Sys() any           { return nil }

func plan(t *testing.T, env adapter.Env, mode adapter.MCPMode, arts ...adapter.Artifact) adapter.Plan {
	t.Helper()
	a := New()
	d, err := a.Detect(context.Background(), env)
	if err != nil || d.Status != adapter.Installed {
		t.Fatalf("Detect: %+v, %v", d, err)
	}
	p, err := a.Plan(context.Background(), adapter.PlanRequest{Env: env, Detection: d, Artifacts: arts, Options: adapter.Options{MCPMode: mode}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// server names a server the way the core would. The adapter does not look at the prefix (the
// contract test checks the real one), and R2 keeps internal/brand out of reach here.
func server(code string, s adapter.MCPServer) adapter.MCPServer {
	s.Resource = resource.Ref{Kind: resource.KindMCP, Code: code, Version: 1}
	s.EntryName = "pfx-" + code
	return s
}

var stdio = server("probe", adapter.MCPServer{Transport: adapter.Stdio, Command: "probe-mcp", Args: []string{"--stdio"}})

// Which file the user-scope servers go into, as checked on Claude Code 2.1.330.
func TestMCPFile(t *testing.T) {
	for _, c := range []struct {
		name  string
		files []string
		vars  map[string]string
		want  string
	}{
		{"default", nil, nil, "~/.claude.json"},
		{"CLAUDE_CONFIG_DIR inside the home", nil, map[string]string{"CLAUDE_CONFIG_DIR": "/Users/example/work/claude"}, "~/work/claude/.claude.json"},
		{"CLAUDE_CONFIG_DIR outside the home", nil, map[string]string{"CLAUDE_CONFIG_DIR": "/opt/claude"}, "/opt/claude/.claude.json"},
		{"CLAUDE_CONFIG_DIR is the default directory", nil, map[string]string{"CLAUDE_CONFIG_DIR": "/Users/example/.claude"}, "~/.claude/.claude.json"},
		// A relative value depends on where Claude Code is started; it is taken as unset.
		{"relative CLAUDE_CONFIG_DIR", nil, map[string]string{"CLAUDE_CONFIG_DIR": "claude"}, "~/.claude.json"},
		{"legacy file", []string{"~/.claude/.config.json", "~/.claude.json"}, nil, "~/.claude/.config.json"},
		{"legacy file in a moved root", []string{"/opt/claude/.config.json"}, map[string]string{"CLAUDE_CONFIG_DIR": "/opt/claude"}, "/opt/claude/.config.json"},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := map[string][]byte{}
			for _, f := range c.files {
				files[f] = []byte("{}")
			}
			p := plan(t, fakeEnv(files, c.vars), adapter.MCPNativeEnv, stdio)
			if len(p.Ops) != 1 {
				t.Fatalf("plan: %+v", p)
			}
			k := p.Ops[0].(adapter.SetKey)
			if k.File != c.want || k.Format != adapter.JSON || !reflect.DeepEqual(k.Path, []string{"mcpServers", stdio.EntryName}) {
				t.Errorf("set-key %s %s %v, want %s json [mcpServers %s]", k.File, k.Format, k.Path, c.want, stdio.EntryName)
			}
		})
	}
}

// When it cannot be told whether the legacy file exists, nothing is written to either file.
func TestMCPFileUnknown(t *testing.T) {
	env := fakeEnv(map[string][]byte{"~/.claude/.config.json": nil, "~/.claude.json": []byte("{}")}, nil)
	p := plan(t, env, adapter.MCPNativeEnv, stdio)
	if len(p.Ops) != 0 || len(p.Skips) != 1 || p.Skips[0].Reason != adapter.ConfigUnreadable {
		t.Fatalf("plan: %+v", p)
	}
}

// The entry shapes of docs/architecture/04a-platforms.md, as claude mcp add --scope user wrote
// them, with secrets as ${NAME}.
func TestEntries(t *testing.T) {
	http := server("issues", adapter.MCPServer{
		Transport: adapter.HTTP,
		URL:       "https://issues.example.com/mcp",
		Headers: map[string]adapter.Value{
			"Authorization": {Literal: "Bearer ", Secret: "ISSUES_TOKEN"},
			"X-Team":        {Literal: "docs"},
		},
	})
	local := server("notes", adapter.MCPServer{
		Transport: adapter.Stdio,
		Command:   "notes-mcp",
		Args:      []string{"--dir", "/srv/notes"},
		Env:       map[string]adapter.Value{"NOTES_KEY": {Secret: "NOTES_KEY"}, "MODE": {Literal: "ro"}},
	})
	p := plan(t, fakeEnv(nil, nil), adapter.MCPNativeEnv, http, local)
	if len(p.Skips) != 0 || len(p.Ops) != 2 {
		t.Fatalf("plan: %+v", p)
	}
	want := []any{
		map[string]any{"type": "http", "url": "https://issues.example.com/mcp", "headers": map[string]any{"Authorization": "Bearer ${ISSUES_TOKEN}", "X-Team": "docs"}},
		map[string]any{"type": "stdio", "command": "notes-mcp", "args": []any{"--dir", "/srv/notes"}, "env": map[string]any{"MODE": "ro", "NOTES_KEY": "${NOTES_KEY}"}},
	}
	for i, op := range p.Ops {
		if got := op.(adapter.SetKey).Value; !reflect.DeepEqual(got, want[i]) {
			t.Errorf("entry %d:\n got %#v\nwant %#v", i, got, want[i])
		}
	}
	if http.Headers["Authorization"].Secret != "ISSUES_TOKEN" {
		t.Error("Plan changed the artifact it was given")
	}
}

func TestCapabilities(t *testing.T) {
	a := New()
	d, _ := a.Detect(context.Background(), fakeEnv(nil, nil))
	got := a.Capabilities(d)
	want := adapter.Capabilities{Skills: true, Commands: true, Agents: true, MCP: adapter.MCPCaps{Stdio: true, HTTP: true, EnvRef: true}}
	if got != want {
		t.Errorf("capabilities %+v, want %+v", got, want)
	}
}

func TestNotDelivered(t *testing.T) {
	for _, c := range []struct {
		name   string
		mode   adapter.MCPMode
		s      adapter.MCPServer
		reason adapter.SkipReason
		detail string
	}{
		{
			"a secret outside native-env", adapter.MCPLauncher,
			server("a", adapter.MCPServer{Transport: adapter.Stdio, Command: "a", Env: map[string]adapter.Value{"K": {Secret: "A_KEY"}}}),
			adapter.NeedsSecretSupport, "A_KEY",
		},
		{
			"a credential Claude Code never sends to a remote server", adapter.MCPNativeEnv,
			server("b", adapter.MCPServer{Transport: adapter.HTTP, URL: "https://b.example.com", Headers: map[string]adapter.Value{"Authorization": {Literal: "Bearer ", Secret: "NPM_TOKEN"}}}),
			adapter.NeedsSecretSupport, "NPM_TOKEN",
		},
		{
			"${ in a literal argument", adapter.MCPNativeEnv,
			server("c", adapter.MCPServer{Transport: adapter.Stdio, Command: "c", Args: []string{"--home=${HOME}"}}),
			adapter.Unsupported, "${",
		},
		{
			"${ in front of a secret", adapter.MCPNativeEnv,
			server("d", adapter.MCPServer{Transport: adapter.HTTP, URL: "https://d.example.com", Headers: map[string]adapter.Value{"X": {Literal: "${A}", Secret: "D_KEY"}}}),
			adapter.Unsupported, "${",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := plan(t, fakeEnv(nil, nil), c.mode, c.s, stdio)
			if len(p.Ops) != 1 || p.Ops[0].Ref() != stdio.Resource {
				t.Errorf("ops: %+v", p.Ops)
			}
			if len(p.Skips) != 1 || p.Skips[0].Resource != c.s.Resource || p.Skips[0].Reason != c.reason || !strings.Contains(p.Skips[0].Detail, c.detail) {
				t.Errorf("skips: %+v", p.Skips)
			}
		})
	}
}

// The NPM_TOKEN rule is about remote servers only: a local process gets it.
func TestCredentialForStdio(t *testing.T) {
	s := server("e", adapter.MCPServer{Transport: adapter.Stdio, Command: "e", Env: map[string]adapter.Value{"NPM_TOKEN": {Secret: "NPM_TOKEN"}}})
	p := plan(t, fakeEnv(nil, nil), adapter.MCPNativeEnv, s)
	if len(p.Ops) != 1 || len(p.Skips) != 0 {
		t.Fatalf("plan: %+v", p)
	}
}

func TestPlanNotInstalled(t *testing.T) {
	_, err := New().Plan(context.Background(), adapter.PlanRequest{Env: fakeEnv(nil, nil)})
	if err == nil {
		t.Fatal("Plan for a tool that was not detected succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Plan(ctx, adapter.PlanRequest{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

// The single entry (MCPSingleEntry): the core hands one stdio server that starts the command
// line's proxy, and it becomes one plain user-scope entry, in the shape of any stdio server
// without secrets.
func TestSingleEntry(t *testing.T) {
	single := adapter.MCPServer{Resource: resource.Ref{Kind: resource.KindMCP, Code: "entry"}, EntryName: "entry",
		Transport: adapter.Stdio, Command: "/opt/example/bin/tool", Args: []string{"mcp", "serve"}}
	p := plan(t, fakeEnv(nil, nil), adapter.MCPSingleEntry, single)
	if len(p.Skips) != 0 || len(p.Ops) != 1 {
		t.Fatalf("plan: %+v", p)
	}
	k := p.Ops[0].(adapter.SetKey)
	want := map[string]any{"type": "stdio", "command": "/opt/example/bin/tool", "args": []any{"mcp", "serve"}, "env": map[string]any{}}
	if k.File != "~/.claude.json" || strings.Join(k.Path, ".") != "mcpServers.entry" || !reflect.DeepEqual(k.Value, want) {
		t.Errorf("entry: %s %v %#v", k.File, k.Path, k.Value)
	}
	// A server that still needs a secret has no place in this mode.
	secret := server("notes", adapter.MCPServer{Transport: adapter.Stdio, Command: "notes-mcp", Env: map[string]adapter.Value{"K": {Secret: "NOTES_KEY"}}})
	if p := plan(t, fakeEnv(nil, nil), adapter.MCPSingleEntry, secret); len(p.Ops) != 0 || len(p.Skips) != 1 || p.Skips[0].Reason != adapter.NeedsSecretSupport {
		t.Errorf("secret: %+v", p)
	}
}
