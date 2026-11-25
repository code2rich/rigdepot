// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package checktest is the test suite for check rules, driven by samples that must be hit and
// samples that must not be. Version: v0.
//
// A rule's own test file is a few lines:
//
//	func TestMain(m *testing.M) { checktest.Main(m) }
//
//	func TestRule(t *testing.T) { checktest.Run(t, New(), "testdata") }
//
// Each sample is a directory laid out as a small resource repository, so that it can hold a
// resource of any kind, and more than one when a rule compares resources. Each rule has at least
// one sample that must be hit and one that looks similar and must not be:
//
//	testdata/
//	├── hit/with-dotenv/skills/demo/        SKILL.md  .env
//	└── miss/only-example/skills/demo/      SKILL.md  .env.example
//
// Run checks more than hit and miss: see its documentation.
//
// Unlike the package it tests, this package depends on the core. It is only ever imported from
// test files.
package checktest
