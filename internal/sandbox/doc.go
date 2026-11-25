// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package sandbox previews a plan by executing it in a temporary copy of the touched files and
// diffing before and after.
//
// A preview is not a guess. The operations of a plan are carried out for real, by the same
// executor that later writes the real files, only rooted at a temporary home directory that
// holds copies of the files the plan touches. What changed there is what would change on the
// real computer.
//
// The steps, all done by Preview:
//
//  1. Collect the targets: the target of every operation, plus the paths of the manifest entries
//     the run will undo (Request.Touches).
//  2. Create a sandbox, readable only by its owner, under the temporary directory of the local
//     data directory, and copy into it those targets that exist, at the same relative paths,
//     together with the directories that lead to them, and nothing else: never the whole home
//     directory. A file whose name is one of the tools' credential file names is never copied,
//     even when it is a declared target.
//  3. Snapshot the sandbox, let the Applier carry out the plan there, snapshot it again.
//  4. Compare the snapshots and translate every path back to where it lies on the real computer.
//  5. Remove the sandbox.
//
// Exec operations are not carried out in a preview: a tool's own command may use the network and
// cannot be confined to a directory. They are returned in Result.NotRun.
//
// Paths are written as in package ext/adapter: "~/…" inside the home directory, absolute
// otherwise, with forward slashes. Targets outside the home directory (a tool root moved by an
// environment variable) are mirrored under a directory of their own inside the sandbox.
//
// This package writes nothing but its sandboxes, and those only inside the directory it is given
// (the local data directory's tmp/). The copies may contain account details (~/.claude.json
// does), so a sandbox is removed as soon as the preview is done, and New removes leftovers of
// earlier runs that ended without cleaning up.
package sandbox
