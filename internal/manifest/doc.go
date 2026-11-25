// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package manifest reads and writes the local manifest: cross-process locking, the intent log,
// format upgrades and digests.
//
// The manifest (home.ManifestFile) records every place the command-line program wrote. Undoing a
// write and detecting drift look at nothing else, so the package keeps four promises:
//
//   - Not being able to read is not knowing. A manifest that does not exist is empty: the
//     program never ran here. A manifest that exists but cannot be opened, does not parse, does
//     not match schema/manifest/v1, or has a newer format version than this program knows is
//     unknown: Read returns an error that satisfies errors.Is(err, ErrUnknown), and the caller
//     refuses to write or uninstall (exit code 9). It is never treated as an empty manifest,
//     which would write things twice or forget to undo them.
//   - Writes are atomic: a temporary file in the same directory, flushed, renamed over the old
//     one. The manifest as it was before is kept as home.ManifestPrevious.
//   - One program at a time. Changing the manifest needs the lock (Store.Lock), which the
//     operating system holds for the process: flock on macOS and Linux, LockFileEx on Windows.
//     It is released when the process ends, however it ends. The methods that change anything
//     are on Locked, so code without the lock cannot call them.
//   - No gap between writing and recording. Every write is announced in the intent log
//     (home.JournalFile) before it starts and recorded in the manifest when it is done. After a
//     crash, Locked.Recover settles what the log still holds: a target in the announced final
//     state is recorded, a target in its former state is forgotten, anything else is left for
//     a person to look at.
//
// Unknown fields in the manifest, at the top and in entries, are kept when the manifest is
// written again, so that an older program does not throw away what a newer one recorded.
package manifest
