// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adaptertest

import (
	"flag"
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

// updateFlag is -update: write golden/ from what the adapter does instead of comparing with it.
// Another package of the test binary may define the same flag, so it is only defined once.
const updateFlag = "update"

func init() {
	if flag.Lookup(updateFlag) == nil {
		flag.Bool(updateFlag, false, "write the golden files instead of comparing with them")
	}
}

func updating() bool {
	f := flag.Lookup(updateFlag)
	return f != nil && f.Value.String() == "true"
}

// Run runs the contract tests for one adapter against the fixtures in dir, which is laid out as
// the package documentation describes. Each home is a subtest; each failure names the assertion
// it belongs to in brackets, such as "[uninstall]".
//
// With -update (go test ./… -run TestContract -update) it writes golden/ from what the adapter
// does instead of comparing with it. Read every file it wrote before committing it: the golden
// files are the review of the adapter's output.
func Run(t *testing.T, a adapter.Adapter, dir string) {
	t.Helper()
	runSuite(tReporter{t}, a, dir, updating())
}
