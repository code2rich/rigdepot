// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package all registers the optional modules, one build-tagged file per module so that any of them
// can be compiled out.
//
// A module with the ID <id> is registered in maybe_<id>.go:
//
//	//go:build !rgd_omit_<id> && !rgd_core_only
//
//	package all
//
//	import (
//		"github.com/code2rich/rigdepot/ext/module"
//		"github.com/code2rich/rigdepot/modules/<id>"
//	)
//
//	func init() { module.Register(<id>.New()) }
//
// In the tag, hyphens of the ID become underscores. The structure check (go run ./tools/dev check)
// fails when a file here lacks that constraint or a module under modules/ is not registered.
package all
