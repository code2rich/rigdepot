// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adaptertest

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// check names one assertion of the contract. Every failure the suite reports carries the name of
// the assertion that failed, so that a broken adapter can be shown to fail exactly the assertion
// that guards against it.
type check string

const (
	checkFixtures      check = "fixtures"      // the fixtures are laid out as documented
	checkDetect        check = "detect"        // the fixture home is detected as an installed tool
	checkPlanPure      check = "plan-pure"     // Plan writes nothing and runs nothing
	checkDeterministic check = "deterministic" // the same input gives the same plan
	checkTargets       check = "targets"       // every target carries the prefix and lies in a declared root
	checkPreview       check = "preview"       // the preview changes what the real write changes
	checkGolden        check = "golden"        // the home after a sync equals golden/, byte for byte
	checkOthers        check = "others"        // nothing that was there before is changed, but for our keys
	checkIdempotent    check = "idempotent"    // a second sync does nothing
	checkDrift         check = "drift"         // a file changed by hand is reported and left alone
	checkLocal         check = "local"         // what is written into local directories is neither drift nor overwritten
	checkUninstall     check = "uninstall"     // uninstalling restores the home byte for byte
	checkUnreadable    check = "unreadable"    // broken-config: nothing written, ConfigUnreadable reported
	checkOccupied      check = "occupied"      // occupied: nothing overwritten, the taken names reported
	checkCore          check = "core"          // the core refused or failed an operation
)

// reporter is the part of *testing.T the suite uses. It lets the suite's own tests run the suite
// against a broken adapter and look at which assertions failed, without failing themselves.
type reporter interface {
	Helper()
	Logf(format string, args ...any)
	// fail records a failed assertion and carries on.
	fail(c check, format string, args ...any)
	// fatal records a failed assertion and ends the current (sub)test.
	fatal(c check, format string, args ...any)
	// run runs f as a subtest.
	run(name string, f func(r reporter))
	// tempDir returns a new empty directory that is removed after the test.
	tempDir() string
}

type tReporter struct{ t *testing.T }

func (r tReporter) Helper()                         { r.t.Helper() }
func (r tReporter) Logf(format string, args ...any) { r.t.Helper(); r.t.Logf(format, args...) }
func (r tReporter) tempDir() string                 { return r.t.TempDir() }

func (r tReporter) fail(c check, format string, args ...any) {
	r.t.Helper()
	r.t.Errorf("[%s] %s", c, fmt.Sprintf(format, args...))
}

func (r tReporter) fatal(c check, format string, args ...any) {
	r.t.Helper()
	r.t.Fatalf("[%s] %s", c, fmt.Sprintf(format, args...))
}

func (r tReporter) run(name string, f func(r reporter)) {
	r.t.Helper()
	r.t.Run(name, func(t *testing.T) { f(tReporter{t}) })
}

// failure is one failed assertion, as a recorder keeps it.
type failure struct {
	test  string
	check check
	msg   string
}

// recorder is a reporter that records failures instead of failing a test.
type recorder struct {
	name     string
	base     string // where temporary directories go
	failures *[]failure
	log      *strings.Builder
}

type stopTest struct{}

func newRecorder(base string) recorder {
	return recorder{base: base, failures: &[]failure{}, log: &strings.Builder{}}
}

func (r recorder) Helper() {}
func (r recorder) Logf(format string, args ...any) {
	fmt.Fprintf(r.log, r.name+": "+format+"\n", args...)
}

func (r recorder) fail(c check, format string, args ...any) {
	*r.failures = append(*r.failures, failure{test: r.name, check: c, msg: fmt.Sprintf(format, args...)})
}

func (r recorder) fatal(c check, format string, args ...any) {
	r.fail(c, format, args...)
	panic(stopTest{})
}

func (r recorder) run(name string, f func(r reporter)) {
	sub := r
	sub.name = r.name + "/" + name
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(stopTest); !ok {
				panic(p)
			}
		}
	}()
	f(sub)
}

func (r recorder) tempDir() string {
	dir, err := os.MkdirTemp(r.base, "case-")
	if err != nil {
		panic(err)
	}
	return dir
}

// checks returns the assertions that failed, each once.
func (r recorder) checks() map[check]int {
	out := map[check]int{}
	for _, f := range *r.failures {
		out[f.check]++
	}
	return out
}
