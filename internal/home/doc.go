// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package home owns the layout, permissions, backups and kept files of the local data directory.
//
// The local data directory is the product's own directory inside the user's home directory
// (brand.Home), or wherever the environment variable brand.EnvPrefix+"HOME" points:
//
//	config.toml            local configuration
//	manifest.json          the manifest: every place written. Package manifest reads and writes it
//	manifest.json.1        the manifest as it was before the last write
//	manifest.journal       the intent log: writes that were started and not yet recorded
//	lock                   the cross-process lock: one program at a time changes things
//	state/                 desired state, consent, local selection, pending operations
//	cache/                 repository clones and bundles
//	backup/<time>/         configuration files as they were before they were first changed
//	kept/<time>/           files a person had changed, kept when they were removed or overwritten
//	tmp/                   temporary home directories of previews
//	secrets/               the local copy of personal secrets, always encrypted
//	logs/                  the log
//
// The directory is 0700 and the files in it are 0600 on macOS and Linux. On Windows the
// directory inherits the ACL of the home directory, which only the user, SYSTEM and the
// administrators can read.
//
// Everything this package writes lies inside the local data directory. Writing anywhere else in
// the home directory is the job of package executor alone (rule R7).
package home
