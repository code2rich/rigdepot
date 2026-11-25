// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package resource

import (
	"io/fs"
	"regexp"
)

// Digest identifies one state of a resource's content, written "sha256:" followed by 64
// lower-case hexadecimal digits. Versions, the local manifest and drift detection all compare
// digests.
//
// The digest of a resource is defined so that anyone can recompute it in a terminal: one line
// per file, "<sha256 of the file>  <path>", sorted by path in byte order, and the SHA-256 of
// that text:
//
//	find . -type f | LC_ALL=C sort | sed 's|^\./||' | xargs sha256sum | sha256sum
//
// Paths use forward slashes. Only paths and file contents count; permissions, modification times
// and owners do not, because Windows cannot report an executable bit and the digest has to be
// the same on every platform. A single-file resource is digested as a directory with one file.
// The core computes digests; nothing in ext/ needs to.
type Digest string

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Valid reports whether d is a well-formed digest.
func (d Digest) Valid() bool {
	return digestPattern.MatchString(string(d))
}

// Bundle is one resource together with its content, the way the core hands it around: which
// resource it is, its metadata, the digest of its files, and the files.
//
// On the wire and in the server's storage a bundle is a deterministic tar archive addressed by
// the SHA-256 of its own bytes. That encoding belongs to the core; code written against ext/
// only ever sees the files.
type Bundle struct {
	Ref    Ref
	Meta   Meta
	Digest Digest

	// Files holds the resource's files, read-only, laid out the same way everywhere a
	// resource's files are passed around (kind.Kind.Parse, check.Input):
	//
	//   - For a directory-shaped kind the root is the resource's directory, so a skill has
	//     "SKILL.md", "meta.yaml" and "references/…" at the top.
	//   - For a single-file kind the root holds exactly one file, named code plus the kind's
	//     extension, such as "jira.yaml".
	//
	// Paths are relative, use forward slashes, and never contain symbolic links: the core has
	// checked them before a bundle exists.
	Files fs.FS
}
