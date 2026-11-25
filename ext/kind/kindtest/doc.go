// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package kindtest is the test suite for resource kinds, driven by valid and invalid fixtures.
// Version: v0.
//
// A kind's own test file is a few lines:
//
//	func TestMain(m *testing.M) { kindtest.Main(m) }
//
//	func TestKind(t *testing.T) { kindtest.Run(t, New(), "testdata") }
//
// # Fixtures
//
// The fixtures are resources of the kind, written the way they are in a resource repository:
//
//	testdata/
//	├── rigdepot.yaml       optional: the repository configuration the fixtures are read with,
//	│                       for the stages and secrets they name; "schema: 1" when missing
//	├── valid/              resources that must be read, parsed and rendered
//	│   └── <code>          a directory (skills/<code>/) or a file (mcp/<code>.yaml), as the
//	│                       kind's Layout says
//	└── invalid/            resources that must be refused
//	    └── <field>--<code> refused because of <field>, which the refusal has to name:
//	                        server.url--secret-in-url.yaml, SKILL.md--missing/
//
// Each fixture is read on its own, the way the core reads a repository (internal/repoformat):
// the metadata, the kind's schema for a single-file kind, then Parse. An invalid fixture passes
// when it is refused for any of these reasons and one of the reasons names the field, either as
// the field the problem is about or in the message.
//
// # Assertions
//
// Every valid resource is rendered for a range of tools, from one that takes everything to one
// that takes nothing, by walking the kind's forms as the core does: the first form whose needs
// the tool meets and whose Render does not return kind.ErrNotApplicable. Each failure is tagged
// with the assertion's name:
//
//	[fixtures]       the fixtures are laid out as described above
//	[schema]         Schema is not empty, is JSON, and compiles as the core compiles it
//	[forms]          there is a form; each has a Render and a Degraded that is a message key
//	[valid]          every valid fixture is read and parsed
//	[invalid]        every invalid fixture is refused, and a reason names its field
//	[render]         a form that applies returns artifacts and no error other than
//	                 ErrNotApplicable
//	[names]          every artifact is for the resource and named with the prefix given; one
//	                 artifact is named prefix plus code; another prefix changes the names by the
//	                 prefix and nothing else; no two artifacts of the same shape share a name
//	[artifacts]      every artifact fits the tool (a stdio server only for a tool that takes
//	                 stdio, …) and is well formed (a skill directory has a SKILL.md and relative
//	                 paths and well-formed local directories, and carries its form's Degraded)
//	[secrets]        no MCP server holds a ${secret:…} reference as text: in its command,
//	                 arguments, URL, environment or headers a secret is adapter.Value.Secret
//	[deterministic]  the same resource and tool give the same artifacts
//	[delivered]      a tool that takes everything gets every valid resource
//
// Unlike the package it tests, this package depends on the core: it reads the fixtures with
// internal/repoformat, so that a kind is checked the way the programs will use it. It is only
// ever imported from test files.
package kindtest
