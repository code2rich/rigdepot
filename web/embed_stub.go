// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build !webdist

package web

import "embed"

// files is the page shown when the console was not built into the program.
//
//go:embed stub
var files embed.FS

const root = "stub"

// Built reports whether FS is the built console rather than the stub page.
const Built = false
