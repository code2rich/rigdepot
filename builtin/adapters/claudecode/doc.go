// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package claudecode is the adapter for Claude Code.
//
// Most of it is profile.yaml: where skills, slash commands, sub-agents and MCP servers go, checked
// against Claude Code 2.1.330. The code adds what a profile cannot say:
//
//   - Which file holds the user-scope MCP servers. It is ~/.claude.json, next to ~/.claude, but
//     when CLAUDE_CONFIG_DIR is set it moves into that directory with everything else; and a
//     .config.json in the configuration root, left by older versions, wins over both.
//   - Personal secrets. Claude Code expands ${NAME} from its environment in the command, args,
//     env, url and headers of a user-scope server, so in adapter.MCPNativeEnv a secret is written
//     as a reference and the person sets the variable. A literal that contains "${" would be
//     expanded too and cannot be escaped, so such a server is not delivered.
package claudecode
