// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package check is the check engine: it runs a rule set against resources and aggregates what
// the rules found.
//
// rgd check and the server's import and upload run the same engine over the same rules. The
// engine receives each resource in memory (Resource: its reference, metadata and file contents)
// and hands a rule nothing but that: the rule's Input.Files is an in-memory, read-only file
// system, so a rule cannot see any other file, whatever it tries.
//
// # From a repository
//
// FromRepo turns what internal/repoformat accepted into the engine's resources. What the reader
// refused (unsafe paths, missing metadata, a file that does not match its schema, a kind that
// cannot parse it) never reaches a rule; Refused reports every reason as a must-fix finding of
// the structure rule, so that rgd check and the server's import give one list of problems
// whichever of the two found them. The keys of those findings are check.structure.repo.<code>.
//
// FindingIDs gives each finding the identifier a waiver refers to: it follows the content the
// finding is about, not its line number.
//
// # What the engine guarantees
//
//   - Every result carries the version of the rule set that produced it, and an engine refuses a
//     rule set without one.
//   - A rule that cannot finish (it returns an error, panics, or reports a finding that points at
//     a file the resource does not have) does not pass: its failure becomes a finding at the
//     rule's own severity, never an empty result.
//   - Findings come back in a stable order: by file, line, rule and message.
//
// # Not here yet
//
// The grace period after a rule set is upgraded (new must-fix problems shown as advisory for one
// period) and false-positive annotations both need what earlier checks found, which the server
// stores. They belong to the server's catalog together with that storage and are not part of
// this package yet.
package check
