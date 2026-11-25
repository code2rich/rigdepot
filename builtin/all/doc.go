// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package all is the registry for built-ins that both programs need: adapters, resource kinds and
// check rules, one line each.
//
// Built-in implementations do not register themselves. Each exports New, and this package holds
// one line per item, in one file per class: adapters.go, kinds.go and checks.go. A program gets
// all of them with a blank import:
//
//	import _ "github.com/code2rich/rigdepot/builtin/all"
//
// To see what a program contains, read this directory. An item that exists under builtin/ but is
// missing here fails the structure check, which names the file and the line to add.
package all
