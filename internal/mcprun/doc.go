// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package mcprun launches MCP servers with secrets injected and serves the single-entry MCP proxy.
//
// The AI tools never see a personal secret: their configuration holds a command of the
// command-line program, and this package fills the secrets in on the person's computer, at the
// moment it connects (docs: subsystems/secrets.md, "运行路径"). Three ways in:
//
//   - Run (rgd mcp run <code>), one entry per server in the tool's configuration. A stdio
//     server is started with the secrets in its environment and the tool's standard input and
//     output; an HTTP server is bridged: the tool speaks MCP over this process's standard input
//     and output, and the messages go to the server over HTTPS with the secrets in the request
//     headers. Nothing listens on a port: a local proxy would let every program on the computer
//     use the person's tokens.
//   - Serve (rgd mcp serve), the single entry: one MCP server over standard input and output
//     that connects to every server delivered natively and offers all their tools, each named
//     ToolName(code, tool).
//   - Call (rgd mcp call <code> <tool> '<JSON>'), one call, for servers delivered on demand as a
//     skill: the skill tells the AI to run it.
//
// What a server is comes from the sync: Keep writes mcp/<code>.json in the local data directory
// for every MCP server the desired state delivers, with secrets as references (kind.Connector).
// The values come from a SecretSource; the command line's opens the sealed copies of package
// localsecrets with the device key.
//
// A secret that is missing is a clear error, not a server that fails to start: Run then serves
// a stand-in that answers every tool call with what is missing and how to set it, and Serve
// offers such stand-ins in place of the server's tools. The AI tells the person; the person sets
// the secret in a terminal or in the browser, never in the conversation.
package mcprun
