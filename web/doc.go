// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package web embeds the web console into the server program. There are two
// builds, chosen by a build tag, so that the Go code builds and tests without
// Node:
//
//   - go build -tags webdist: the console built by "go run ./tools/dev web"
//     (web/dist/) is embedded. Releases are built this way.
//   - without the tag: a single page that says the console was not built and
//     how to build it.
//
// The sources of the console (web/src) are TypeScript; see web/README.md.
package web

import "io/fs"

// FS returns the console's files with index.html at the root.
func FS() fs.FS {
	sub, err := fs.Sub(files, root)
	if err != nil {
		panic(err) // root is a constant directory that go:embed guarantees
	}
	return sub
}
