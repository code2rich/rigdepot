// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package codex is the adapter for Codex.
//
// Skills go to ~/.agents/skills, which Codex reads wherever CODEX_HOME points; MCP servers are
// tables [mcp_servers.<name>] in $CODEX_HOME/config.toml (default ~/.codex). Codex has no slash
// commands or sub-agent files of its own that were verified, so workflows and agents arrive as
// skills. Where each fact comes from is in profile.yaml.
//
// The profile does detection and skills. The code does the MCP servers, because FromProfile
// cannot read TOML: it reads config.toml to tell whether an entry can be added (a file that does
// not parse is reported as ConfigUnreadable, an mcp_servers written as an inline table as
// ConfigUnwritable), and writes each server in Codex's own fields. A personal secret becomes the
// name of an environment variable the person sets: bearer_token_env_var or env_http_headers over
// HTTP, env_vars for a local server.
package codex
