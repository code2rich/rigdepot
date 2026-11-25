// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build !unix

package bundle

import "io/fs"

// linkCount returns 1: the file information Go gives on this platform does not carry the
// number of names a file has. Hard links in a bundle or an archive are still refused by their
// tar entry type.
func linkCount(fs.FileInfo) uint64 { return 1 }
