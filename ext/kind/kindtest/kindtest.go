// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package kindtest

import (
	"testing"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// Main isolates the test binary and runs its tests: a temporary home directory, no network, and
// stand-ins that fail in place of the real AI tools. Every test package of a kind starts with
// it:
//
//	func TestMain(m *testing.M) { kindtest.Main(m) }
//
// It never returns.
func Main(m *testing.M) {
	testenv.Main(m)
}

// Run runs one kind against the fixtures in dir: every resource under valid/ must parse and
// render, every resource under invalid/ must be refused.
//
// The assertions are not written yet. Until they are, Run fails the test, so that no kind can
// look as if its samples had been checked.
func Run(t *testing.T, k kind.Kind, dir string) {
	t.Helper()
	t.Fatalf("kindtest.Run is not implemented yet: kind %q was not run against %s", k.ID(), dir)
}
