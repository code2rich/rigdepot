// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build webdist

package web

import "embed"

// files is the built console: run "go run ./tools/dev web" first, then build
// with -tags webdist.
//
//go:embed all:dist
var files embed.FS

const root = "dist"

// Built reports whether FS is the built console rather than the stub page.
const Built = true
