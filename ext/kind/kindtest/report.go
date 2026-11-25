// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package kindtest

import (
	"fmt"
	"strings"
	"testing"
)

// check names one assertion of the suite. Every failure the suite reports carries the name of
// the assertion that failed, so that a broken kind can be shown to fail exactly the assertion
// that guards against it.
type check string

const (
	checkFixtures      check = "fixtures"      // the fixtures are laid out as documented
	checkSchema        check = "schema"        // Schema is JSON that compiles as the core compiles it
	checkForms         check = "forms"         // Forms lists forms that can be used
	checkValid         check = "valid"         // every valid fixture is read and parsed
	checkInvalid       check = "invalid"       // every invalid fixture is refused, naming its field
	checkRender        check = "render"        // a form that applies renders artifacts without an error
	checkNames         check = "names"         // names carry the prefix given, and the resource
	checkArtifacts     check = "artifacts"     // every artifact is well formed and fits the tool
	checkSecrets       check = "secrets"       // no artifact carries a secret reference as text
	checkDeterministic check = "deterministic" // the same resource and tool give the same artifacts
	checkDelivered     check = "delivered"     // a tool that takes everything gets every resource
)

// reporter is the part of *testing.T the suite uses. It lets the suite's own tests run the suite
// against a broken kind and look at which assertions failed, without failing themselves.
type reporter interface {
	Helper()
	// fail records a failed assertion and carries on.
	fail(c check, format string, args ...any)
	// fatal records a failed assertion and ends the current (sub)test.
	fatal(c check, format string, args ...any)
	// run runs f as a subtest.
	run(name string, f func(r reporter))
}

type tReporter struct{ t *testing.T }

func (r tReporter) Helper() { r.t.Helper() }

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
	failures *[]failure
}

type stopTest struct{}

func newRecorder() recorder { return recorder{failures: &[]failure{}} }

func (recorder) Helper() {}

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

// checks returns how often each assertion failed.
func (r recorder) checks() map[check]int {
	out := map[check]int{}
	for _, f := range *r.failures {
		out[f.check]++
	}
	return out
}

// String lists the failures, one per line, for a test that did not expect them.
func (r recorder) String() string {
	var b strings.Builder
	for _, f := range *r.failures {
		first, _, _ := strings.Cut(f.msg, "\n")
		fmt.Fprintf(&b, "  %s [%s] %s\n", f.test, f.check, first)
	}
	return b.String()
}
