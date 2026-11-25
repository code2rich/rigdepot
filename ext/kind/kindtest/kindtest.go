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

// Run runs one kind against the fixtures in dir, which is laid out as the package documentation
// describes. Each fixture is a subtest; each failure names the assertion it belongs to in
// brackets, such as "[invalid]".
func Run(t *testing.T, k kind.Kind, dir string) {
	t.Helper()
	runSuite(tReporter{t}, k, dir)
}
