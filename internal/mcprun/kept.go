// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package mcprun

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/home"
)

// formatVersion is the version of the files Keep writes.
const formatVersion = 1

// The transports of a Server.
const (
	Stdio = "stdio"
	HTTP  = "http"
	SSE   = "sse"
)

var (
	// ErrUnknownServer: no MCP server with this code has been synced to this computer.
	ErrUnknownServer = errors.New("no such MCP server on this computer")
	// ErrUnreadable: a file of this package could not be read or parsed; what it holds is
	// unknown.
	ErrUnreadable = errors.New("the MCP server's file cannot be read")
)

// Server is an MCP server as kept on this computer: mcp/<code>.json.
type Server struct {
	Schema int    `json:"schema"`
	Code   string `json:"code"`
	Name   string `json:"name"`
	// Digest is the content digest of the resource as synced.
	Digest    string           `json:"digest,omitempty"`
	Transport string           `json:"transport"`
	Command   string           `json:"command,omitempty"`
	Args      []string         `json:"args,omitempty"`
	Env       map[string]Value `json:"env,omitempty"`
	URL       string           `json:"url,omitempty"`
	Headers   map[string]Value `json:"headers,omitempty"`
	// OnDemand: delivered as a skill that calls it through rgd mcp call; Serve leaves it out.
	OnDemand bool   `json:"onDemand"`
	Tools    []Tool `json:"tools,omitempty"`
}

// Value is a value of the environment or a header: a literal, or a literal followed by the value
// of a personal secret.
type Value struct {
	Literal string `json:"literal,omitempty"`
	Secret  string `json:"secret,omitempty"`
}

// Tool is a tool the repository lists for the server.
type Tool struct {
	Name    string `json:"name"`
	Summary string `json:"summary,omitempty"`
	Writes  bool   `json:"writes,omitempty"`
}

// Secrets returns the names of the personal secrets the server needs, sorted, each once.
func (s Server) Secrets() []string {
	var out []string
	for _, m := range []map[string]Value{s.Env, s.Headers} {
		for _, v := range m {
			if v.Secret != "" && !slices.Contains(out, v.Secret) {
				out = append(out, v.Secret)
			}
		}
	}
	slices.Sort(out)
	return out
}

func values(in map[string]adapter.Value) map[string]Value {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]Value, len(in))
	for k, v := range in {
		out[k] = Value{Literal: v.Literal, Secret: v.Secret}
	}
	return out
}

// Kept says what Keep did.
type Kept struct {
	// Written and Removed are resource codes, sorted.
	Written, Removed []string
}

// Keep writes the MCP servers of a desired state to mcp/<code>.json, so that rgd mcp run, serve
// and call find them without the repository or the server, and removes the files of servers the
// state no longer delivers. The file of a server that could not be read this time stays as it
// was: its state is unknown, not gone.
func Keep(data home.Dir, st *desired.State) (Kept, error) {
	var out Kept
	dir := data.Join(home.MCPDir)
	isServer := func(r desired.Resource) bool { _, ok := r.Spec.(kind.Connector); return ok }
	if !slices.ContainsFunc(st.Resources, isServer) {
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
	} else if _, err := data.EnsureSub(home.MCPDir); err != nil {
		return out, err
	}
	keep := map[string]bool{}
	for _, f := range st.Failed {
		keep[f.Ref.Code] = true
	}
	for _, r := range st.Resources {
		c, ok := r.Spec.(kind.Connector)
		if !ok {
			continue
		}
		conn := c.Connection()
		s := Server{
			Schema: formatVersion, Code: r.Ref.Code, Name: r.Meta.Name, Digest: string(r.Digest),
			Transport: conn.Transport, Command: conn.Command, Args: slices.Clone(conn.Args),
			Env: values(conn.Env), URL: conn.URL, Headers: values(conn.Headers), OnDemand: conn.OnDemand,
		}
		for _, t := range conn.Tools {
			s.Tools = append(s.Tools, Tool{Name: t.Name, Summary: t.Summary, Writes: t.Writes})
		}
		raw, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			return out, err
		}
		if err := home.WriteFileAtomic(filepath.Join(dir, r.Ref.Code+".json"), append(raw, '\n'), home.FilePerm); err != nil {
			return out, err
		}
		keep[r.Ref.Code] = true
		out.Written = append(out.Written, r.Ref.Code)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, err
	}
	for _, e := range entries {
		code, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || keep[code] || !resource.ValidCode(code) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return out, err
		}
		out.Removed = append(out.Removed, code)
	}
	slices.Sort(out.Written)
	slices.Sort(out.Removed)
	return out, nil
}

// Load reads the MCP server with the given code as last synced.
func Load(data home.Dir, code string) (Server, error) {
	if !resource.ValidCode(code) {
		return Server{}, fmt.Errorf("%w: %q", ErrUnknownServer, code)
	}
	p := data.Join(home.MCPDir, code+".json")
	raw, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Server{}, fmt.Errorf("%w: %s", ErrUnknownServer, code)
	}
	if err != nil {
		return Server{}, fmt.Errorf("%w: %s: %v", ErrUnreadable, p, err)
	}
	var s Server
	if err := json.Unmarshal(raw, &s); err != nil {
		return Server{}, fmt.Errorf("%w: %s: %v", ErrUnreadable, p, err)
	}
	if s.Schema < 1 || s.Schema > formatVersion || s.Code != code {
		return Server{}, fmt.Errorf("%w: %s: not an MCP server this program can read", ErrUnreadable, p)
	}
	switch s.Transport {
	case Stdio:
		if s.Command == "" {
			return Server{}, fmt.Errorf("%w: %s: no command", ErrUnreadable, p)
		}
	case HTTP, SSE:
		if s.URL == "" {
			return Server{}, fmt.Errorf("%w: %s: no address", ErrUnreadable, p)
		}
	default:
		return Server{}, fmt.Errorf("%w: %s: unknown transport %q", ErrUnreadable, p, s.Transport)
	}
	return s, nil
}

// LoadAll reads every MCP server synced to this computer, by code. A file that cannot be read
// fails the whole: which servers there are is then unknown.
func LoadAll(data home.Dir) ([]Server, error) {
	entries, err := os.ReadDir(data.Join(home.MCPDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	var out []Server
	for _, e := range entries {
		code, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || !resource.ValidCode(code) {
			continue
		}
		s, err := Load(data, code)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// resolve fills the secrets into values.
func resolve(in map[string]Value, secrets map[string][]byte) map[string]string {
	out := make(map[string]string, len(in))
	for _, k := range slices.Sorted(maps.Keys(in)) {
		v := in[k]
		out[k] = v.Literal
		if v.Secret != "" {
			out[k] += string(secrets[v.Secret])
		}
	}
	return out
}
