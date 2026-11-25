// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adaptertest

import (
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// Main isolates the test binary and runs its tests: a temporary home directory, no network, and
// stand-ins that fail in place of the real AI tools. Every test package of an adapter starts
// with it:
//
//	func TestMain(m *testing.M) { adaptertest.Main(m) }
//
// It never returns.
func Main(m *testing.M) {
	testenv.Main(m)
}

// Run runs the contract tests for one adapter against the fixtures in dir, which is laid out as
// the package documentation describes.
//
// The assertions are not written yet. Until they are, Run fails the test, so that no adapter
// can look as if it had passed a contract nobody checked.
func Run(t *testing.T, a adapter.Adapter, dir string) {
	t.Helper()
	t.Fatalf("adaptertest.Run is not implemented yet: the contract of adapter %q was not checked against %s", a.ID(), dir)
}
