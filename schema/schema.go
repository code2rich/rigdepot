// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package schema

import (
	"embed"
	"io/fs"
)

// The schemas, by their path in FS. The path is also what follows the common base address in a
// schema's $id.
const (
	// Repo describes rigdepot.yaml, the configuration at the root of a resource repository.
	Repo = "repo/v1/rigdepot.schema.json"
	// Meta describes the metadata every resource carries: meta.yaml, or the "meta" key of a
	// single-file resource. Other schemas refer to its definitions.
	Meta = "repo/v1/meta.schema.json"
	// Manifest describes the local manifest, the record of everything written on a computer.
	Manifest = "manifest/v1/manifest.schema.json"
	// Profile describes profile.yaml, the declarative part of a tool adapter.
	Profile = "adapter/v1/profile.schema.json"
)

//go:embed repo/v1/*.schema.json manifest/v1/*.schema.json adapter/v1/*.schema.json
var files embed.FS

// FS returns the schemas as a read-only file system, each under the path given by the constants
// above.
//
// Schemas refer to each other with relative references, so a validator needs all of them
// registered under their $id before it compiles one.
func FS() fs.FS {
	return files
}
