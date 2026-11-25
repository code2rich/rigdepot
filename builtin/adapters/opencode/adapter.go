// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/adapter/profileyaml"
	"github.com/code2rich/rigdepot/ext/resource"
)

// New returns the adapter for OpenCode. The profile does detection, the configuration root
// (XDG_CONFIG_HOME), skills, slash commands and most of sub-agents. The code below does what the
// profile cannot say:
//
//   - Where they go depends on the version. OpenCode 2 lists them under mcp.servers; OpenCode 1
//     (and 0) directly under mcp. See serversPath for how the version is decided.
//   - A local server's command and its arguments are one array, and the shape is OpenCode's own:
//     {"type":"local","command":[…],"environment":{…}} and
//     {"type":"remote","url":…,"headers":{…}}.
//   - A personal secret is written as OpenCode's own reference to an environment variable,
//     "{env:NAME}" (MCPCaps.EnvRef), so the file never holds its value.
//   - A literal that contains "{env:" or "{file:" is refused: OpenCode replaces those anywhere in
//     the file's text before parsing it, so a resource could make OpenCode read any file or
//     variable and send it to a remote server.
//   - A sub-agent's front matter says "mode: subagent"; without it OpenCode 2 makes it a primary
//     agent.
//   - When both opencode.json and opencode.jsonc exist, no server is written (ConfigUnwritable):
//     OpenCode reads and merges both, and its own commands disagree about which one to change.
func New() adapter.Adapter {
	p := profileyaml.MustParse(profileYAML)
	return &opencode{Adapter: adapter.FromProfile(p), p: p}
}

type opencode struct {
	adapter.Adapter
	p adapter.Profile
}

// Capabilities is the profile's, plus EnvRef: OpenCode replaces "{env:NAME}" in any string of its
// configuration by the variable's value (an empty string when it is not set).
func (o *opencode) Capabilities(d adapter.Detection) adapter.Capabilities {
	c := o.Adapter.Capabilities(d)
	c.MCP.EnvRef = c.MCP.Stdio || c.MCP.HTTP
	return c
}

// Plan leaves everything but MCP servers to the profile, adds the mode of sub-agents, and plans
// the servers itself.
func (o *opencode) Plan(ctx context.Context, req adapter.PlanRequest) (adapter.Plan, error) {
	var servers []adapter.MCPServer
	var others []adapter.Artifact
	for _, art := range req.Artifacts {
		if s, ok := art.(adapter.MCPServer); ok {
			servers = append(servers, s)
		} else {
			others = append(others, art)
		}
	}
	rest := req
	rest.Artifacts = others
	plan, err := o.Adapter.Plan(ctx, rest)
	if err != nil {
		return plan, err
	}
	if err := o.subagentMode(&plan, req.Detection); err != nil {
		return adapter.Plan{}, err
	}
	if len(servers) == 0 {
		return plan, nil
	}

	caps := o.Capabilities(req.Detection)
	skip := func(s adapter.MCPServer, reason adapter.SkipReason, detail string) {
		plan.Skips = append(plan.Skips, adapter.Skip{Resource: s.Resource, Reason: reason, Detail: detail})
	}
	var cfg *mcpConfig // read once, for the first server the tool takes
	for _, s := range servers {
		if (s.Transport == adapter.Stdio && !caps.MCP.Stdio) || (s.Transport == adapter.HTTP && !caps.MCP.HTTP) {
			skip(s, adapter.Unsupported, "the tool does not take MCP servers over "+string(s.Transport))
			continue
		}
		if cfg == nil {
			if cfg, err = o.readConfig(req.Env, req.Detection); err != nil {
				return adapter.Plan{}, err
			}
		}
		if cfg.reason != "" {
			skip(s, cfg.reason, cfg.detail)
			continue
		}
		value, reason, detail, err := serverValue(s, req.Options.MCPMode == adapter.MCPNativeEnv)
		if err != nil {
			return adapter.Plan{}, fmt.Errorf("adapter %s: MCP server %s: %w", o.p.ID, s.Resource, err)
		}
		if reason != "" {
			skip(s, reason, detail)
			continue
		}
		plan.Ops = append(plan.Ops, adapter.SetKey{
			Resource: s.Resource,
			File:     cfg.file,
			Format:   o.p.MCP.Format,
			Path:     append(slices.Clone(cfg.path), s.EntryName),
			Value:    value,
		})
	}
	return plan, nil
}

// subagentMode adds "mode: subagent" to the front matter of every sub-agent the profile planned.
// Without it OpenCode makes the agent a primary one, offered next to its own agents for the whole
// session, instead of one the AI hands work to.
func (o *opencode) subagentMode(plan *adapter.Plan, d adapter.Detection) error {
	if o.p.Agents == nil {
		return nil
	}
	dir, err := adapter.ExpandPath(o.p.Agents.Dir, d.Roots)
	if err != nil {
		return fmt.Errorf("adapter %s: %w", o.p.ID, err)
	}
	for i, op := range plan.Ops {
		f, ok := op.(adapter.WriteFile)
		if !ok || !strings.HasPrefix(f.Path, dir+"/") {
			continue
		}
		// The profile writes "---\n<fields>---\n\n<body>": the field goes before the second "---".
		end := bytes.Index(f.Data, []byte("\n---\n"))
		if !bytes.HasPrefix(f.Data, []byte("---\n")) || end < 0 {
			return fmt.Errorf("adapter %s: %s: the sub-agent has no front matter", o.p.ID, f.Path)
		}
		f.Data = slices.Concat(f.Data[:end+1], []byte("mode: subagent\n"), f.Data[end+1:])
		plan.Ops[i] = f
	}
	return nil
}

// mcpConfig is what Plan found out about the configuration file the servers go into.
type mcpConfig struct {
	file   string
	path   []string           // the key the servers are listed under
	reason adapter.SkipReason // non-empty when no server can be written
	detail string
}

// readConfig finds the configuration file, reads it, and decides under which key the servers go.
// A file that cannot be read or does not parse is ConfigUnreadable; two files, or a key on the way
// that holds something other than an object, is ConfigUnwritable.
func (o *opencode) readConfig(env adapter.Env, d adapter.Detection) (*mcpConfig, error) {
	var existing []string
	var data []byte
	last := ""
	for _, tmpl := range o.p.MCP.Files {
		file, err := adapter.ExpandPath(tmpl, d.Roots)
		if err != nil {
			return nil, fmt.Errorf("adapter %s: %w", o.p.ID, err)
		}
		last = file
		if env.ReadFile == nil {
			return &mcpConfig{file: file, reason: adapter.ConfigUnreadable, detail: file + ": cannot be read"}, nil
		}
		b, err := env.ReadFile(file)
		switch {
		case err == nil:
			existing = append(existing, file)
			data = b
		case errors.Is(err, fs.ErrNotExist):
		default:
			return &mcpConfig{file: file, reason: adapter.ConfigUnreadable, detail: file + ": cannot be read"}, nil
		}
	}
	switch len(existing) {
	case 0:
		return &mcpConfig{file: last, path: serversPath(d.Version, nil)}, nil // created by the executor
	case 1:
	default:
		return &mcpConfig{file: existing[0], reason: adapter.ConfigUnwritable,
			detail: "both " + strings.Join(existing, " and ") + " exist; OpenCode reads both, and it is not known which one to change"}, nil
	}

	c := &mcpConfig{file: existing[0]}
	doc, offset, err := decodeJSONC(data)
	if err != nil {
		c.reason, c.detail = adapter.ConfigUnreadable, fmt.Sprintf("%s: not valid jsonc at byte %d", c.file, offset)
		return c, nil
	}
	c.path = serversPath(d.Version, doc)
	var cur any = doc
	for i := 0; ; i++ {
		obj, ok := cur.(map[string]any)
		if !ok {
			c.reason, c.detail = adapter.ConfigUnwritable, fmt.Sprintf("%s: %s is not an object", c.file, keyPath(c.path[:i]))
			return c, nil
		}
		if i == len(c.path) {
			return c, nil
		}
		next, ok := obj[c.path[i]]
		if !ok {
			return c, nil // the executor creates the missing levels
		}
		cur = next
	}
}

// serversPath returns the key the servers are listed under, from the version Detect found:
//
//   - 2 or later: mcp.servers. OpenCode 2 still reads servers listed directly under mcp (it calls
//     them legacy entries and converts them), but its own commands write mcp.servers.
//   - 1 or 0: mcp. These versions take every key under mcp as a server, so mcp.servers would be
//     a server named "servers" without a type, and the whole file would be refused.
//
// When the version is not known (the tool did not answer --version, or not in a form we
// understand), the file decides: a file that already has an mcp.servers object belongs to
// OpenCode 2. Otherwise the servers go directly under mcp, the one place every version reads.
func serversPath(version string, doc any) []string {
	v2 := []string{"mcp", "servers"}
	v1 := []string{"mcp"}
	if major, ok := majorVersion(version); ok {
		if major >= 2 {
			return v2
		}
		return v1
	}
	top, _ := doc.(map[string]any)
	mcp, _ := top["mcp"].(map[string]any)
	if _, ok := mcp["servers"].(map[string]any); ok {
		return v2
	}
	return v1
}

// majorVersion returns the major version of a version such as "2.4.1".
func majorVersion(v string) (int, bool) {
	head, _, ok := strings.Cut(v, ".")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(head)
	return n, err == nil && n >= 0
}

// serverValue builds OpenCode's entry for a server. With envRefs, a secret becomes "{env:NAME}";
// without, a server that needs one is reported as NeedsSecretSupport. A server that cannot be
// written safely is reported with a reason; an error is a defect in what the resource kind made.
func serverValue(s adapter.MCPServer, envRefs bool) (map[string]any, adapter.SkipReason, string, error) {
	var refused []string // strings OpenCode would substitute into
	check := func(what, text string) {
		if strings.Contains(text, "{env:") || strings.Contains(text, "{file:") {
			refused = append(refused, what)
		}
	}
	secret := ""
	values := func(field string, m map[string]adapter.Value) (map[string]any, error) {
		out := make(map[string]any, len(m))
		for _, k := range slices.Sorted(maps.Keys(m)) {
			v := m[k]
			check(field+" "+k, k)
			check(field+" "+k, v.Literal)
			if v.Secret == "" {
				out[k] = v.Literal
				continue
			}
			if !resource.ValidSecretName(v.Secret) {
				return nil, fmt.Errorf("%q is not a secret name", v.Secret)
			}
			if secret == "" {
				secret = v.Secret
			}
			out[k] = v.Literal + "{env:" + v.Secret + "}"
		}
		return out, nil
	}

	var value map[string]any
	switch s.Transport {
	case adapter.Stdio:
		check("command", s.Command)
		command := []any{s.Command}
		for i, a := range s.Args {
			check(fmt.Sprintf("argument %d", i+1), a)
			command = append(command, a)
		}
		env, err := values("environment variable", s.Env)
		if err != nil {
			return nil, "", "", err
		}
		value = map[string]any{"type": "local", "command": command, "environment": env}
	case adapter.HTTP:
		check("url", s.URL)
		headers, err := values("header", s.Headers)
		if err != nil {
			return nil, "", "", err
		}
		value = map[string]any{"type": "remote", "url": s.URL, "headers": headers}
	default:
		return nil, adapter.Unsupported, "unknown transport " + strconv.Quote(string(s.Transport)), nil
	}
	if len(refused) > 0 {
		return nil, adapter.Unsupported, "contains {env: or {file: (" + strings.Join(refused, ", ") +
			"), which OpenCode would replace by a variable or a file's content", nil
	}
	if secret != "" && !envRefs {
		return nil, adapter.NeedsSecretSupport, "refers to the personal secret " + secret + ", and in this MCP mode it cannot be taken from the environment", nil
	}
	return value, "", "", nil
}

func keyPath(p []string) string {
	if len(p) == 0 {
		return "the top level"
	}
	return strings.Join(p, ".")
}

// decodeJSONC parses a JSON or JSONC file (OpenCode reads both names as JSONC). On failure it
// returns the byte offset of the error.
func decodeJSONC(data []byte) (any, int64, error) {
	dec := json.NewDecoder(bytes.NewReader(adapter.StandardJSON(data, true)))
	var doc any
	if err := dec.Decode(&doc); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			return nil, syn.Offset, err
		}
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return nil, int64(len(data)), err // the file ends in the middle of a value, or is empty
		}
		return nil, dec.InputOffset(), err
	}
	if _, err := dec.Token(); err == nil {
		return nil, dec.InputOffset(), errors.New("more than one value")
	}
	return doc, 0, nil
}
