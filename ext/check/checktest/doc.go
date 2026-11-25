// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package checktest is the test suite for check rules, driven by hit and miss fixtures. Version:
// v0.
//
// A rule's own test file is a few lines:
//
//	func TestMain(m *testing.M) { checktest.Main(m) }
//
//	func TestRule(t *testing.T) { checktest.Run(t, New(), "testdata") }
//
// The fixtures are resources. Each rule has at least one that must be hit and one that looks
// similar and must not be:
//
//	testdata/
//	├── hit/with-dotenv/        SKILL.md  .env
//	└── miss/only-example/      SKILL.md  .env.example
//
// Unlike the package it tests, this package depends on the core. It is only ever imported from
// test files.
//
// Status: Main works. Run is the agreed signature only; the assertions are not written yet, and
// until they are Run fails the test that calls it.
package checktest
