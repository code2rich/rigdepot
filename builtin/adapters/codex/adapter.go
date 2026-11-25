// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package codex

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/adapter/profileyaml"
)

// New returns the adapter for Codex. The profile does detection, skills and the fallbacks; the
// code below does what a profile cannot: MCP servers in config.toml, which FromProfile cannot
// read, written in Codex's own shape, with personal secrets as references to environment
// variables.
func New() adapter.Adapter {
	return &codex{Adapter: adapter.FromProfile(profileyaml.MustParse(profileYAML))}
}

type codex struct{ adapter.Adapter }

// Capabilities adds EnvRef to what the profile says: Codex can take a bearer token, a request
// header or a variable for a local server from the environment.
func (c *codex) Capabilities(d adapter.Detection) adapter.Capabilities {
	caps := c.Adapter.Capabilities(d)
	if caps.MCP.Stdio || caps.MCP.HTTP {
		caps.MCP.EnvRef = true
	}
	return caps
}

// Plan leaves everything but MCP servers to the profile and plans the servers itself.
func (c *codex) Plan(ctx context.Context, req adapter.PlanRequest) (adapter.Plan, error) {
	if err := ctx.Err(); err != nil {
		return adapter.Plan{}, err
	}
	if req.Detection.Status != adapter.Installed {
		return adapter.Plan{}, fmt.Errorf("adapter %s: cannot plan for a tool whose status is %s", c.ID(), req.Detection.Status)
	}
	var servers []adapter.MCPServer
	rest := req
	rest.Artifacts = nil
	for _, art := range req.Artifacts {
		if s, ok := art.(adapter.MCPServer); ok {
			servers = append(servers, s)
			continue
		}
		rest.Artifacts = append(rest.Artifacts, art)
	}
	plan, err := c.Adapter.Plan(ctx, rest)
	if err != nil {
		return adapter.Plan{}, err
	}
	if len(servers) == 0 {
		return plan, nil
	}
	if err := c.planMCP(req, servers, &plan); err != nil {
		return adapter.Plan{}, err
	}
	return plan, nil
}

// serversKey is the table Codex lists its MCP servers in.
const serversKey = "mcp_servers"

func (c *codex) planMCP(req adapter.PlanRequest, servers []adapter.MCPServer, plan *adapter.Plan) error {
	caps := c.Capabilities(req.Detection)
	skip := func(s adapter.MCPServer, reason adapter.SkipReason, detail string) {
		plan.Skips = append(plan.Skips, adapter.Skip{Resource: s.Resource, Reason: reason, Detail: detail})
	}
	var cfg *configState // read once, on the first server that gets that far
	for _, s := range servers {
		if (s.Transport == adapter.Stdio && !caps.MCP.Stdio) || (s.Transport == adapter.HTTP && !caps.MCP.HTTP) || (s.Transport != adapter.Stdio && s.Transport != adapter.HTTP) {
			skip(s, adapter.Unsupported, "the tool does not take MCP servers over "+string(s.Transport))
			continue
		}
		if err := checkName(s.EntryName); err != nil {
			return fmt.Errorf("adapter %s: MCP server %s: %w", c.ID(), s.Resource, err)
		}
		if cfg == nil {
			var err error
			if cfg, err = c.readConfig(req.Env, req.Detection.Roots); err != nil {
				return err
			}
		}
		if cfg.reason != "" {
			skip(s, cfg.reason, cfg.detail)
			continue
		}
		value, secretProblem := serverValue(s, req.Options.MCPMode)
		if secretProblem != "" {
			skip(s, adapter.NeedsSecretSupport, secretProblem)
			continue
		}
		plan.Ops = append(plan.Ops, adapter.SetKey{
			Resource: s.Resource,
			File:     cfg.file,
			Format:   adapter.TOML,
			Path:     []string{serversKey, s.EntryName},
			Value:    value,
		})
	}
	return nil
}

// configState is what Plan found out about config.toml.
type configState struct {
	file   string             // the file to write, in the package form
	reason adapter.SkipReason // non-empty when no server can be written
	detail string
}

// readConfig reads config.toml through Env and decides whether servers can be added to it. A
// file that cannot be read or does not parse is unknown, not empty: no server is planned for it.
func (c *codex) readConfig(env adapter.Env, roots map[string]string) (*configState, error) {
	p := c.Profile()
	if p.MCP == nil || len(p.MCP.Files) != 1 {
		return nil, fmt.Errorf("adapter %s: the profile must name exactly one MCP configuration file", c.ID())
	}
	file, err := adapter.ExpandPath(p.MCP.Files[0], roots)
	if err != nil {
		return nil, fmt.Errorf("adapter %s: %w", c.ID(), err)
	}
	st := &configState{file: file}
	if env.ReadFile == nil {
		st.reason, st.detail = adapter.ConfigUnreadable, file+": cannot be read"
		return st, nil
	}
	data, err := env.ReadFile(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return st, nil // the executor creates it
	case err != nil:
		st.reason, st.detail = adapter.ConfigUnreadable, file+": cannot be read"
		return st, nil
	}
	problem, err := checkConfig(data)
	switch {
	case err != nil:
		st.reason, st.detail = adapter.ConfigUnreadable, file+": "+err.Error()
	case problem != "":
		st.reason, st.detail = adapter.ConfigUnwritable, file+": "+problem
	}
	return st, nil
}

// checkName refuses an entry name that is not a single TOML bare key, which is what every
// prefixed name the core gives is. Anything else is a defect in the resource kind.
func checkName(name string) error {
	if name == "" {
		return errors.New("the entry name is empty")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return fmt.Errorf("the entry name %q is not a bare TOML key", name)
		}
	}
	return nil
}

// serverValue builds the table of one [mcp_servers.<name>] entry, in the fields Codex reads
// (checked against codex-cli 0.172.0, see profile.yaml):
//
//	stdio: command, args, env (literal values), env_vars (names of variables forwarded as they are)
//	http:  url, http_headers (literal values), env_http_headers (header -> variable),
//	       bearer_token_env_var (variable whose value is sent as "Authorization: Bearer …")
//
// A personal secret becomes the name of an environment variable the person sets. When it cannot
// be expressed that way it returns why, and the server is not delivered: a secret's value never
// goes into the file.
func serverValue(s adapter.MCPServer, mode adapter.MCPMode) (map[string]any, string) {
	envRefs := mode == "" || mode == adapter.MCPNativeEnv
	noRef := func(where, secret string) string {
		return fmt.Sprintf("%s refers to the personal secret %s, and the MCP mode %q has no way to deliver it", where, secret, mode)
	}
	v := map[string]any{}
	switch s.Transport {
	case adapter.Stdio:
		v["command"] = s.Command
		if len(s.Args) > 0 {
			args := make([]any, len(s.Args))
			for i, a := range s.Args {
				args[i] = a
			}
			v["args"] = args
		}
		env := map[string]any{}
		var forward []any
		for _, k := range sortedKeys(s.Env) {
			val := s.Env[k]
			switch {
			case val.Secret == "":
				env[k] = val.Literal
			case !envRefs:
				return nil, noRef("the variable "+k, val.Secret)
			case val.Literal != "":
				return nil, fmt.Sprintf("the variable %s is the personal secret %s with text in front of it; Codex can only pass a variable on unchanged", k, val.Secret)
			case k != val.Secret:
				return nil, fmt.Sprintf("the variable %s is the personal secret %s; Codex passes a variable on under its own name only (env_vars), so the server would not find it", k, val.Secret)
			default:
				forward = append(forward, k)
			}
		}
		if len(env) > 0 {
			v["env"] = env
		}
		if len(forward) > 0 {
			v["env_vars"] = forward
		}
	case adapter.HTTP:
		v["url"] = s.URL
		literal, fromEnv := map[string]any{}, map[string]any{}
		for _, h := range sortedKeys(s.Headers) {
			val := s.Headers[h]
			switch {
			case val.Secret == "":
				literal[h] = val.Literal
			case !envRefs:
				return nil, noRef("the header "+h, val.Secret)
			case val.Literal == "":
				fromEnv[h] = val.Secret
			case strings.EqualFold(h, "Authorization") && strings.EqualFold(val.Literal, "Bearer ") && v["bearer_token_env_var"] == nil:
				v["bearer_token_env_var"] = val.Secret
			default:
				return nil, fmt.Sprintf("the header %s is the personal secret %s with %q in front of it; Codex can only send a variable as it is, or as a bearer token", h, val.Secret, val.Literal)
			}
		}
		if len(literal) > 0 {
			v["http_headers"] = literal
		}
		if len(fromEnv) > 0 {
			v["env_http_headers"] = fromEnv
		}
	}
	return v, ""
}

func sortedKeys(m map[string]adapter.Value) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
