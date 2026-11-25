// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package knowledge is the knowledge base resource kind: knowledge/<code>.yaml, a pointer to a
// directory of a Git repository. schema.json has the shape.
//
// The resource repository, and the server, hold only the pointer. The content is fetched by the
// command line onto each person's computer, into the local data directory
// (<data>/knowledge/<code>/), with the person's own git and so with exactly the access the person
// has (kind.Pulled; the sync engine does the fetching). Nothing of it passes through the server.
//
// What reaches an AI tool is a skill that says what the knowledge base is about and where its
// documents are on this computer, and asks the AI to search them; it holds none of the content.
// The source type "mcp" of the product design (a knowledge base behind a registered MCP server)
// is not read yet.
package knowledge
