// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package claudecode

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"slices"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/adapter/profileyaml"
)

// New returns the adapter for Claude Code: the profile does everything it can say, and this
// type adds the two things it cannot (see the package documentation).
func New() adapter.Adapter {
	p := profileyaml.MustParse(profileYAML)
	return &claudeCode{Adapter: adapter.FromProfile(p), p: p}
}

type claudeCode struct {
	adapter.Adapter
	p adapter.Profile
}

// The files Claude Code keeps its own state in, MCP servers of the user scope among them.
const (
	stateFile  = ".claude.json"
	legacyFile = ".config.json" // in the configuration root; wins over stateFile when it exists
)

// Capabilities adds MCPCaps.EnvRef to what the profile says: Claude Code expands ${VAR} in the
// command, args, env, url and headers of a user-scope server.
func (c *claudeCode) Capabilities(d adapter.Detection) adapter.Capabilities {
	caps := c.Adapter.Capabilities(d)
	caps.MCP.EnvRef = caps.MCP.Stdio || caps.MCP.HTTP
	return caps
}

// Plan plans with the profile adapter, after two changes the profile cannot express: the MCP
// configuration file is the one Claude Code really reads on this computer, and, in
// MCPNativeEnv, a personal secret becomes a ${NAME} reference.
func (c *claudeCode) Plan(ctx context.Context, req adapter.PlanRequest) (adapter.Plan, error) {
	if err := ctx.Err(); err != nil {
		return adapter.Plan{}, err
	}
	if req.Detection.Status != adapter.Installed {
		return c.Adapter.Plan(ctx, req) // refuses, with the profile adapter's error
	}
	file, err := c.mcpFile(req.Env)
	if err != nil {
		return adapter.Plan{}, err
	}
	p := c.p
	mcp := *c.p.MCP
	mcp.Files = []string{file}
	p.MCP = &mcp

	var skips []adapter.Skip
	arts := make([]adapter.Artifact, 0, len(req.Artifacts))
	for _, art := range req.Artifacts {
		s, ok := art.(adapter.MCPServer)
		if !ok {
			arts = append(arts, art)
			continue
		}
		s, skip := envRefs(s, req.Options.MCPMode)
		if skip != nil {
			skips = append(skips, *skip)
			continue
		}
		arts = append(arts, s)
	}
	req.Artifacts = arts
	plan, err := adapter.FromProfile(p).Plan(ctx, req)
	if err != nil {
		return adapter.Plan{}, err
	}
	plan.Skips = append(skips, plan.Skips...)
	return plan, nil
}

// mcpFile returns the path template of the file Claude Code reads its user-scope MCP servers
// from. Claude Code keeps .claude.json in the home directory, next to ~/.claude, unless
// CLAUDE_CONFIG_DIR is set: then it is inside that directory, with everything else. Before both,
// a .config.json in the configuration root, the name of older versions, is used instead when it
// exists (checked on 2.1.330: claude mcp add writes there and claude mcp list ignores
// .claude.json).
func (c *claudeCode) mcpFile(env adapter.Env) (string, error) {
	config := c.p.Roots["config"]
	legacy := "{config}/" + legacyFile
	roots, err := adapter.ResolveRoots(map[string]adapter.Root{
		"config": config,
		// Where .claude.json is: the same variable, but the home directory without it.
		"state": {Env: config.Env, Join: config.Join, Default: "~"},
	}, env)
	if err != nil {
		return "", err
	}
	path, _ := adapter.ExpandPath(legacy, roots)
	if env.Stat == nil {
		return legacy, nil // cannot look: reading it fails too, and the plan says so
	}
	// When it cannot be told whether the file is there, which file Claude Code reads is unknown
	// too. Name the one it would try first: reading it fails, and every server is reported as
	// unreadable.
	if _, err := env.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		return legacy, nil
	}
	if roots["state"] == "~" {
		return "~/" + stateFile, nil
	}
	// The variable is set, so the configuration root is the same directory.
	return "{config}/" + stateFile, nil
}

// neverExpanded are credential variables Claude Code reads as empty in the url and headers of a
// remote server, also in the user scope, whether they are set or not. The list is not published
// in full; these are the ones its documentation names (mcp page, "Credential variables that read
// as empty") and the ones checked on 2.1.330 against a server on the loopback address.
var neverExpanded = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"AWS_BEARER_TOKEN_BEDROCK",
	"AWS_SECRET_ACCESS_KEY",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"HTTPS_PROXY",
	"NPM_TOKEN",
}

// envRefs rewrites the secrets of an MCP server as ${NAME}, which Claude Code fills in from the
// environment it was started in. It leaves the server as it is outside MCPNativeEnv (the profile
// adapter then reports NeedsSecretSupport), and returns a skip for a server whose meaning
// Claude Code would change.
func envRefs(s adapter.MCPServer, mode adapter.MCPMode) (adapter.MCPServer, *adapter.Skip) {
	skip := func(reason adapter.SkipReason, detail string) (adapter.MCPServer, *adapter.Skip) {
		return s, &adapter.Skip{Resource: s.Resource, Reason: reason, Detail: detail}
	}
	// Claude Code expands ${…} in every one of these fields and offers no way to escape it, so a
	// literal that has one would be filled in from the environment.
	literals := append([]string{s.Command, s.URL}, s.Args...)
	for _, m := range []map[string]adapter.Value{s.Env, s.Headers} {
		for _, k := range slices.Sorted(maps.Keys(m)) {
			literals = append(literals, m[k].Literal)
		}
	}
	for _, l := range literals {
		if strings.Contains(l, "${") {
			return skip(adapter.Unsupported, "a value contains \"${\", which Claude Code would replace with an environment variable")
		}
	}
	if mode != adapter.MCPNativeEnv {
		return s, nil
	}
	refs := func(m map[string]adapter.Value) (map[string]adapter.Value, string) {
		if m == nil {
			return nil, ""
		}
		out := make(map[string]adapter.Value, len(m))
		for _, k := range slices.Sorted(maps.Keys(m)) {
			v := m[k]
			if v.Secret != "" {
				if s.Transport == adapter.HTTP && slices.Contains(neverExpanded, v.Secret) {
					return nil, v.Secret
				}
				v = adapter.Value{Literal: v.Literal + "${" + v.Secret + "}"}
			}
			out[k] = v
		}
		return out, ""
	}
	env, bad := refs(s.Env)
	if bad == "" {
		var headers map[string]adapter.Value
		if headers, bad = refs(s.Headers); bad == "" {
			s.Env, s.Headers = env, headers
			return s, nil
		}
	}
	return skip(adapter.NeedsSecretSupport, "refers to the personal secret "+bad+
		", which Claude Code never passes to a remote server; the secret needs another name")
}
