// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package kindtest is the test suite for resource kinds, driven by valid and invalid fixtures.
// Version: v0.
//
// A kind's own test file is a few lines:
//
//	func TestMain(m *testing.M) { kindtest.Main(m) }
//
//	func TestKind(t *testing.T) { kindtest.Run(t, New(), "testdata") }
//
// The fixtures are resources of the kind: those under testdata/valid/ must parse and render,
// those under testdata/invalid/ must be refused.
//
// Unlike the package it tests, this package depends on the core. It is only ever imported from
// test files.
//
// Status: Main works. Run is the agreed signature only; the assertions are not written yet, and
// until they are Run fails the test that calls it.
package kindtest
