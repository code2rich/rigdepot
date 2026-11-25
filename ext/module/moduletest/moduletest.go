// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package moduletest is the test support for optional modules. Version: v0.
//
// A module may import only the standard library and ext/, so its tests reach the test isolation
// through this package:
//
//	func TestMain(m *testing.M) { moduletest.Main(m) }
//
// A fake Host to start a module against comes with the first real module (M2).
//
// Unlike the package it supports, this package depends on the core. It is only ever imported
// from test files.
package moduletest

import (
	"testing"

	"github.com/code2rich/rigdepot/internal/testenv"
)

// Main runs the tests of a package in a temporary home with the network limited to this
// machine, like every test in this repository.
func Main(m *testing.M) {
	testenv.Main(m)
}
