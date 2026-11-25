// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package bundle holds the files of one resource: an in-memory snapshot that has passed the path
// safety checks, its content digest, and the resource bundle, a deterministic tar archive
// addressed by the SHA-256 of its own bytes.
//
// Every way files come into the programs goes through here, and every one of them applies the
// same checks:
//
//	Snapshot      a directory, or any fs.FS (a work tree, a sub-tree of a repository archive)
//	SnapshotFile  one file, for single-file resource kinds
//	Unpack        a bundle received from the server
//	ReadTree      a whole repository archive (git archive), kept as it is so that a problem in
//	              one resource does not stop the others from being read
//
// # Path safety
//
// A resource is refused, as a whole, when any of its paths breaks a rule (see Rule):
//
//	not-relative    absolute, "..", "\", a drive letter
//	link            symbolic link or hard link
//	special         device, pipe, socket and anything else that is not a file or directory
//	windows-name    a reserved name (CON, NUL, COM1 …), a trailing dot or space, < > : " | ? *
//	                or a control character
//	case-collision  two names in one directory that differ only in case
//	too-long        more than MaxPathBytes bytes; too-deep: more than MaxDepth components
//	too-many-files  more than MaxFiles files; too-large: more than MaxBytes bytes in all
//
// Paths are measured relative to the resource's own root, which is what lands below a tool's
// skill directory.
//
// # Content digest
//
// Digest is the digest of package resource: one line "<sha256>  <path>" per file, sorted by
// path in byte order, and the SHA-256 of that text. It equals what this prints in the
// resource's directory:
//
//	find . -type f | LC_ALL=C sort | sed 's|^\./||' | xargs sha256sum | sha256sum
//
// The product's own marker file (MarkerFile) at the root is not counted, and callers can leave
// out further directories (an agent's local directories).
//
// # Bundle
//
// Pack writes a tar with entries in byte order of their paths, every parent directory before
// its contents, modification times at the Unix epoch, owner 0, and only regular files and
// directories. A file is mode 0755 when any execute bit was set and 0644 otherwise; nothing
// else of the mode is kept. Empty directories are not kept. The same files always give the same
// bytes.
package bundle
