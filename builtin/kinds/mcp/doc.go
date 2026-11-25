// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package mcp is the MCP server resource kind: mcp/<code>.yaml, one server, how it is delivered
// and which tools it offers. schema.json has the shape.
//
// # Secrets
//
// A personal secret is written ${secret:NAME}. It may only be in the values of server.env and
// server.headers, and only at the end of one: "Bearer ${secret:JIRA_API_TOKEN}". Anywhere else
// (the URL, the command, its arguments, or any other field) it is refused, because it would end
// up in process lists, logs or a tool's configuration as text. A parsed server holds the
// reference as structure, adapter.Value{Literal: "Bearer ", Secret: "JIRA_API_TOKEN"}; how it
// is written into a tool is the adapter's business (in M0, the tool's own way of reading an
// environment variable). The secrets a server refers to are reported through kind.SecretUser,
// so that the core checks they are defined in rigdepot.yaml.
//
// # Forms
//
// In order:
//
//  1. Natively, into the tool's MCP configuration, when the server is meant so ("delivery:
//     native", the default) and the tool takes its transport.
//  2. As a skill, when the server is meant to be used on demand ("delivery: on-demand"). The
//     skill lists the server's tools and tells the AI to call them through the command line
//     ("<cli> mcp call <code> <tool> '<JSON>'"). It holds nothing about how the server is
//     reached, and no secret.
//  3. As the same skill, marked with the explanation "mcp.as_skill", when a server meant to be
//     connected natively cannot be: the tool takes no MCP servers or not this transport. An SSE
//     server always ends up here, because the artifacts know no SSE transport. A server that
//     lists no tools is not delivered this way, since the skill would have nothing to say.
package mcp
