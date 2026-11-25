// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package executor applies, verifies and reverts operations (Op); it is the only place that writes
// files outside the local data directory.
//
// Adapters compute operations (package ext/adapter); the executor carries them out. A preview
// and the real write run the same operations through the same executor, against a temporary
// copy of the home directory or the real one: the only difference is the Roots it is given.
//
// # What it checks before it writes
//
//   - The target is written the way ext/adapter describes paths ("~/…" or absolute, forward
//     slashes, no "." or ".." elements) and lies inside one of the executor's roots. Every file
//     operation goes through an os.Root, so neither ".." nor a symbolic link leads outside.
//   - The target lies strictly inside one of the roots the tool's detection reported.
//   - The last element of the target carries the product's prefix (brand.Prefix).
//   - The target is still in the state the plan saw (Observed). If not, nothing is written and
//     the error is ErrChanged.
//
// A target that breaks one of the first three is a defect in the adapter, reported as a
// *DefectError; it is never shown to the person as an ordinary failure.
//
// # How it writes
//
// Each write is announced in the intent log before it starts and recorded in the manifest when
// it has been read back and verified (package manifest). A directory is written next to its
// target under a temporary name and renamed into place; a directory it replaces is first
// renamed aside and removed only once the new one is in place. Any failure before that puts
// the old directory back: the target is either what it was or what it should become. A
// process killed halfway leaves names derived from the intent's id, and Settle puts things
// right on the next run before manifest recovery looks at the target.
//
// Directories created to hold a target (a tool's skills directory that did not exist yet) are
// recorded with the entry and removed again, if empty, when the entry is undone.
//
// A directory gets a marker file (MarkerFile) saying who put it there; it is not part of the
// directory's digest.
//
// # Undoing
//
// Revert removes what a manifest entry records. A target that was changed since it was written
// is copied to kept/<time>/ in the local data directory first; one that is gone already only
// loses its entry. Nothing outside the manifest is ever removed.
//
// # Keys in a tool's configuration file
//
// SetKey edits a file that belongs to the tool and the person, through package configedit: one
// run of bytes changes and every other byte stays as it was. keys.go lists the steps (lock,
// read and compare, parse, edit and check, back up to backup/<time>/, temporary file, compare
// the file again, rename). The prefix rule applies to the last element of the key, and the file
// may be exactly a place the tool's profile names (~/.claude.json) as well as lie inside one of
// its roots. A file that does not parse, or is a symbolic link, is unknown and is not written.
//
// The entry records how to take the key out again byte for byte (manifest.KeyEdit). Revert does
// that while those bytes are still in place, and otherwise removes the key by its meaning, with
// the parents the edit created if they are empty; a file the edit created goes when nothing else
// is in it. Its digest is KeyDigest of the value.
//
// # Not yet
//
// Exec (running the tool's own command) comes later. It has its place in the dispatch and
// returns ErrNotImplemented.
package executor
