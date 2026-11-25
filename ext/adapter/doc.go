// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package adapter is the tool adapter extension point (E1): the Adapter interface, tool profiles,
// artifacts, operations (Op) and plans. Version: v0.
//
// An adapter teaches the product one AI coding tool: how to tell whether it is installed, what
// it supports, and where each thing it supports has to go. Supporting a new tool means writing
// one adapter, and usually most of it is a declarative Profile.
//
// # One chain, four roles
//
//	resource (a directory or file in the resource repository)
//	   │  a resource kind (package kind) picks a form the tool supports
//	   ▼
//	Artifact (tool-independent: skill directory, MCP server, slash command, sub-agent)
//	   │  Adapter.Plan: where this tool keeps such a thing, and in which format
//	   ▼
//	Op (a closed set: write a tree, write a file, set a config key, run the tool's own command)
//	   │  the executor in the core, the only code that writes
//	   ▼
//	manifest entry (every write is recorded; undo and drift detection look at nothing else)
//
// # Adapters compute, they never write
//
// Plan returns operations and the core's executor carries them out. An adapter must not create,
// change or delete a file, run a command or open a network connection; it reads through the Env
// it is given and nothing else. Three things follow from that:
//
//   - Backups, atomic replacement, "a file that does not parse is not written", verification and
//     the manifest are implemented and reviewed once, in the executor. A careless adapter can
//     compute a wrong target, but it cannot write outside what it declared.
//   - A preview and the real write are the same operations run against different roots, so they
//     cannot disagree.
//   - An adapter is a pure function from artifacts to operations and is tested as one.
//
// The set of operations is closed on purpose. If a tool needs something the four operations
// cannot express, the set is extended in this package; there is no "custom" operation.
//
// An adapter never returns an operation that removes something. What has to go is worked out by
// the core, from the manifest and the new plan, and undone by the executor. An adapter does not
// know the manifest exists, so it cannot remove anything that is not in it.
//
// # Paths
//
// Every path in this package is written the same way on every platform: with forward slashes,
// starting with "~/" when it lies inside the user's home directory and absolute otherwise (a
// tool's configuration root can be moved out of the home directory by an environment variable).
// Detection.Roots, the targets of operations and the arguments of Env.ReadFile and Env.Stat all
// use this form. The executor resolves it, so an adapter never has to know whether the home
// directory is the real one or the temporary copy a preview runs in.
//
// # Names
//
// Everything the product writes into a tool carries the product's prefix: directory names, file
// names and the last segment of a configuration key. Artifacts arrive with the prefix already in
// their names, and the executor refuses an operation whose target lacks it. An adapter never
// spells the prefix out.
//
// # Registering
//
// A built-in adapter exports New and is registered with one line in builtin/all. An adapter that
// lives in another repository is registered the same way by the program that links it in.
//
// # Stability
//
// Everything under ext/ is public API. Before 1.0 it may change in breaking ways, and every such
// change is listed in the release notes. From 1.0 on, a breaking change means a new version of
// the package, and the old one stays for at least one major release.
package adapter
