// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package gitrepo takes snapshots of a Git repository by calling the system git (bare clone plus
// archive, no work tree).
//
// The person's own git is used on purpose: their credential helper, SSH agent, proxy, internal
// certificate authorities and insteadOf rules are all configured there, so a repository is read
// with exactly the access the person already has. Nothing here implements Git itself.
//
// # What a snapshot is
//
// Fetcher.Snapshot takes an address, a reference and optionally a directory, and returns the
// commit the reference pointed at and the files of that commit as a read-only, in-memory fs.FS.
// The repository is kept as a bare clone in the cache directory, one per address, and updated
// with git fetch; its content is read with git archive, so no work tree is ever checked out.
//
// The bytes are the bytes stored in the commit. git archive on its own applies the same
// conversions a checkout does (core.autocrlf, the eol, text, ident, filter and working-tree-
// encoding attributes, export-subst and export-ignore), which would make the same resource look
// different on different computers. The cache repository therefore carries an info/attributes
// file that switches all of them off, and core.autocrlf is set to false on every call.
//
// # Safety
//
//   - Commands are run as fixed argument lists, never through a shell.
//   - Only https and ssh addresses are accepted, and local paths and file:// addresses only when
//     Options.AllowLocal says the person gave one explicitly. Every "<transport>::<address>" form
//     (ext::, fd:: …) is refused, and git is told the same through GIT_ALLOW_PROTOCOL, so a
//     redirect or a submodule cannot switch to another transport either.
//   - Git never asks for anything: no terminal prompt, no askpass program, no credential manager
//     dialog, and on Unix no controlling terminal for ssh to prompt on. A repository that needs
//     credentials the person's git cannot supply by itself fails with ErrFetch.
//   - Hooks are switched off, and repository-location variables (GIT_DIR and friends) inherited
//     from the environment are dropped.
//   - Every command has a timeout; when it expires the whole process group is killed.
package gitrepo
