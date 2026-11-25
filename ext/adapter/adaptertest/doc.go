// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package adaptertest is the adapter contract test suite; any adapter, built-in or external, can
// run it against fixture home directories. Version: v0.
//
// An adapter's own test file is a few lines:
//
//	func TestMain(m *testing.M) { adaptertest.Main(m) }
//
//	func TestContract(t *testing.T) { adaptertest.Run(t, New(), "testdata") }
//
// The fixtures are home directories as people really have them, made from files the real tool
// wrote, with account details removed:
//
//	testdata/
//	├── homes/
//	│   ├── fresh/            the tool is installed and has no configuration yet
//	│   ├── user-config/      the person's own configuration: comments, unknown keys, own entries
//	│   ├── crlf-bom/         the same with CRLF line endings and a byte order mark
//	│   ├── broken-config/    a configuration file that does not parse
//	│   └── occupied/         the target name is taken by something that is not ours
//	└── golden/               each home after a sync, byte for byte
//
// For every home the suite asserts what the product promises about any tool: the preview equals
// what is written, a second sync does nothing, a file changed by hand is reported and left
// alone, uninstalling restores the home byte for byte, an unreadable configuration is never
// written, a taken name is never overwritten, every target carries the prefix and lies inside a
// declared root, and Plan neither writes nor uses the network.
//
// Unlike the packages it tests, this package depends on the core: it drives the real executor.
// It is only ever imported from test files.
//
// Status: Main works. Run is the agreed signature only; the assertions are not written yet, and
// until they are Run fails the test that calls it.
package adaptertest
