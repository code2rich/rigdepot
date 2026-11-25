// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package repoformat reads a resource repository in format v1: rigdepot.yaml, every resource's
// metadata, schema validation against the embedded JSON Schemas, the checks that need more than
// one file, and each resource's files and content digest.
//
// Read takes any fs.FS. The programs give it the tree of a git archive (bundle.ReadTree), not a
// work tree: git archive with core.autocrlf and core.eol switched off yields the bytes as
// committed, so a digest does not depend on how a person's Git converts line endings.
//
// # What Read does
//
//  1. rigdepot.yaml: missing, malformed, a newer format, or a requires.rgd above the running
//     version refuses the whole repository with an error. Nothing is read from a repository
//     whose configuration cannot be trusted.
//  2. Each top-level directory owned by a registered kind (kind.Layout.Dir) is read; one no kind
//     owns is skipped with a notice. Dot entries and files at the top are skipped silently.
//  3. Each resource is read on its own. Its files are taken into memory through the path safety
//     checks (bundle.Snapshot, bundle.SnapshotFile), digested, its metadata is read (meta.yaml,
//     or the "meta" key of a single YAML file), validated against the metadata schema and, for
//     a single-file kind, the kind's own schema; stages and secrets are checked against
//     rigdepot.yaml; then the kind parses it, and the secrets its own files refer to
//     (kind.SecretUser, such as ${secret:NAME} in an MCP server) are checked as well. Any
//     problem fails that resource only, with every reason found, each with a file and, where
//     there is one, a line and a field.
//     A kind that keeps directories on the person's computer (kind.LocalKeeper, an agent's
//     memory) has them left out of the digest.
//  4. Codes are unique across kinds; every resource sharing one fails.
//  5. A resource that refers to other resources by code (kind.Referrer: the steps of a
//     workflow, what an agent binds) fails when one of them is not in the repository or failed
//     itself. Options.Alone leaves this out, for reading a resource without its repository.
//
// Unknown fields are reported as notices and block nothing; a field whose name starts with "x-"
// is the author's own and is not reported. A failed resource is unknown, not deleted: code that
// acts on a repository must keep what it had for it.
package repoformat
