// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package adaptertest is the adapter contract test suite; any adapter, built-in or external, can
// run it against fixture home directories. Version: v0.
//
// An adapter's own test file is a few lines:
//
//	func TestMain(m *testing.M) { adaptertest.Main(m) }
//
//	func TestContract(t *testing.T) { adaptertest.Run(t, New(), "testdata") }
//
// # Fixtures
//
// The fixtures are home directories as people really have them, made from files the real tool
// wrote, with account details removed:
//
//	testdata/
//	├── homes/
//	│   ├── fresh/            the tool is installed and has no configuration yet
//	│   ├── user-config/      the person's own configuration: comments, unknown keys, own entries
//	│   ├── crlf-bom/         the same with CRLF line endings and a byte order mark
//	│   ├── broken-config/    a configuration file that does not parse
//	│   ├── occupied/         the target names are taken by things that are not ours
//	│   └── <more>/           situations of this tool (an inline TOML table, a moved root, …)
//	└── golden/
//	    ├── <case>/           the home after a sync, byte for byte (written by -update)
//	    └── <case>.plan       the plan and what the core decided (written by -update)
//
// The first five cases are required. Each case directory holds:
//
//	README.md    which tool and version the files come from, and what the case shows
//	case.json    optional: what the computer looks like apart from the files
//	home/        the home directory itself; may be missing for an empty home
//
// case.json has four optional fields: "goos" (the operating system the adapter is told about),
// "env" (environment variables; a value starting with "~/" is a path inside the home), "bins"
// (the executables on the PATH; by default every one the profile looks for) and "versionOutput"
// (what the tool prints for its version; by default the version it was verified against).
//
// Every adapter is synced with the same artifacts: a skill, a slash command, a sub-agent, an MCP
// server over stdio, one over HTTP and one that needs a personal secret, as far as the adapter's
// capabilities take them; and what the resource kinds make of an agent (a skill directory with
// its own skills and local directories) and, for a tool without slash commands, of a workflow (a
// skill). Their names carry the product's prefix, as the core gives them.
//
// # Assertions
//
// For every home the suite asserts what the product promises about any tool. Each failure is
// tagged with the assertion's name:
//
//	[plan-pure]      Plan writes nothing (neither in the home nor in the process's home directory)
//	                 and runs no command
//	[deterministic]  the same input gives the same plan
//	[targets]        every target carries the prefix and lies in a root or a place the profile
//	                 declares; paths are in the package form
//	[preview]        the preview, run in a temporary copy, changes what the real write changes
//	[golden]         the home after a sync, and the plan, equal golden/
//	[others]         what was there before is unchanged; a configuration file we added keys to
//	                 differs by those keys only
//	[idempotent]     a second sync writes nothing
//	[drift]          a file we wrote, changed by hand, is reported and left alone
//	[local]          what is written into the local directories of a tree we wrote (an agent's
//	                 memory) is not reported as a change, and survives an update
//	[uninstall]      undoing everything restores the home byte for byte, directories included
//	[unreadable]     broken-config: the plan reports ConfigUnreadable, sets no key in the broken
//	                 file, and nothing at all is written
//	[occupied]       occupied: the taken names are reported and nothing of the person's is touched
//
// The network is blocked by Main for the whole test binary, so an adapter that uses it fails
// with the error it gets.
//
// # The core
//
// Unlike the packages it tests, this package depends on the core: it applies, observes and
// reverts operations with the executor and prepares previews with the sandbox, so it checks the
// core along with the adapter. It is only ever imported from test files.
//
// The preview is prepared by internal/sandbox: it copies what the operations touch and what the
// adapter read while planning (every path it gave Env.ReadFile), the adapter plans again in that
// copy, and the executor carries the plan out there.
//
// Keys in configuration files are set by the executor too (package configedit underneath), in
// JSON, JSONC and TOML. The "others" check reads JSON and JSONC itself; for TOML it relies on
// configedit, whose own tests check that only our tables are added.
package adaptertest
