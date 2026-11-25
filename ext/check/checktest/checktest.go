// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package checktest

import (
	"testing"

	"github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// Main isolates the test binary and runs its tests: a temporary home directory, no network, and
// stand-ins that fail in place of the real AI tools. Every test package of a rule starts with
// it:
//
//	func TestMain(m *testing.M) { checktest.Main(m) }
//
// It never returns.
func Main(m *testing.M) {
	testenv.Main(m)
}

// Run runs one rule against the fixtures in dir: every resource under hit/ must produce at least
// one finding, every resource under miss/ none.
//
// The assertions are not written yet. Until they are, Run fails the test, so that no rule can
// look as if its samples had been checked.
func Run(t *testing.T, r check.Rule, dir string) {
	t.Helper()
	t.Fatalf("checktest.Run is not implemented yet: rule %q was not run against %s", r.ID(), dir)
}
