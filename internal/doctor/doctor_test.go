// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/testenv"
	"github.com/code2rich/rigdepot/locales"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// newTestEnv describes a computer whose home directory is a fresh directory inside the test's
// temporary home, with the local data directory at its usual place (not created) and no tools.
func newTestEnv(t *testing.T) *Env {
	t.Helper()
	userHome := filepath.Join(testenv.Current(t).Home, strings.ReplaceAll(t.Name(), "/", "_"))
	if err := os.MkdirAll(userHome, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := home.At(filepath.Join(userHome, brand.Home))
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{}
	return &Env{
		Home:     dir,
		UserHome: userHome,
		GOOS:     runtime.GOOS,
		Getenv:   func(k string) string { return vars[k] },
		LockWait: 50 * time.Millisecond,
	}
}

// mkdata creates the local data directory with the given permissions.
func mkdata(t *testing.T, env *Env) string {
	t.Helper()
	if err := env.Home.Ensure(); err != nil {
		t.Fatal(err)
	}
	return env.Home.Path()
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// run runs one check and returns its results.
func run(t *testing.T, c Check, env *Env) []Result {
	t.Helper()
	r := Run(context.Background(), env, []Check{c})
	if len(r.Results) == 0 {
		t.Fatalf("check %s returned no result", c.ID())
	}
	return r.Results
}

// only runs one check that must return exactly one result, and returns it.
func only(t *testing.T, c Check, env *Env) Result {
	t.Helper()
	rs := run(t, c, env)
	if len(rs) != 1 {
		t.Fatalf("check %s returned %d results, want 1: %+v", c.ID(), len(rs), rs)
	}
	return rs[0]
}

func expect(t *testing.T, r Result, status Status, code string) {
	t.Helper()
	if r.Status != status || r.Code != code {
		t.Errorf("result %s/%s %q: status %s, code %q; want %s, %q", r.Check, r.Subject, r.Message.Key, r.Status, r.Code, status, code)
	}
	if r.Message.IsZero() {
		t.Errorf("result %s/%s has no message", r.Check, r.Subject)
	}
}

// english renders messages for tests that look at the words.
func english(t *testing.T) *i18n.Printer {
	t.Helper()
	cat, err := i18n.Load(locales.FS, "catalog", "check", "cli")
	if err != nil {
		t.Fatal(err)
	}
	return cat.Printer(cat.Match("en"))
}

// The list is the contract with people and scripts: IDs are unique and stable in form, every
// title and message exists in English, and every check answers on an empty computer as well as
// on one where nothing can be read.
func TestChecksAreWellFormed(t *testing.T) {
	p := english(t)
	seen := map[string]bool{}
	for _, c := range Checks() {
		id := c.ID()
		if seen[id] || id == "" || strings.ToLower(id) != id || strings.ContainsAny(id, " _.") {
			t.Errorf("check ID %q is empty, repeated or not lower-case with hyphens", id)
		}
		seen[id] = true
		if p.T(c.Title()) == c.Title() {
			t.Errorf("check %s: the title %q has no English text", id, c.Title())
		}
	}

	empty := newTestEnv(t)
	empty.Git = testenv.NeedGit(t)
	broken := newTestEnv(t)
	broken.Git = filepath.Join(broken.UserHome, "no-such-git")
	broken.Home, broken.HomeErr = home.Dir{}, errors.New("no home")
	broken.UserHome = ""
	for name, env := range map[string]*Env{"empty": empty, "unknown home": broken} {
		r := Run(context.Background(), env, Checks())
		got := map[string]bool{}
		for _, res := range r.Results {
			got[res.Check] = true
			if res.Code == "" || res.Message.IsZero() || res.Title == "" {
				t.Errorf("%s: result %+v lacks a code, message or title", name, res)
			}
			for _, m := range append([]Message{res.Message, res.Hint}, res.Details...) {
				if !m.IsZero() && m.Text(p) == m.Key {
					t.Errorf("%s: message %q has no English text", name, m.Key)
				}
			}
		}
		for id := range seen {
			if !got[id] {
				t.Errorf("%s: check %s returned nothing", name, id)
			}
		}
	}
}

func TestStatus(t *testing.T) {
	var zero Status
	if zero != Unknown {
		t.Error("the zero Status must be Unknown, so that a result nobody filled in is not a pass")
	}
	b, _ := json.Marshal(struct{ S Status }{Warn})
	if string(b) != `{"S":"warn"}` {
		t.Errorf("JSON of a status: %s", b)
	}
	r := Report{Results: []Result{{Status: OK}, {Status: Skipped}, {Status: Warn}}}
	if r.Worst() != Warn || r.Count(OK) != 1 {
		t.Errorf("Worst = %s, Count(OK) = %d", r.Worst(), r.Count(OK))
	}
	r.Results = append(r.Results, Result{Status: Unknown})
	if r.Worst() != Unknown {
		t.Errorf("Unknown must outweigh Warn: %s", r.Worst())
	}
	r.Results = append(r.Results, Result{Status: Fail})
	if r.Worst() != Fail {
		t.Errorf("Fail must outweigh everything: %s", r.Worst())
	}
	if (Report{}).Worst() != OK {
		t.Error("an empty report is OK")
	}
}

// Run names every result after its check, whatever the check put there.
func TestRunFillsCheckAndTitle(t *testing.T) {
	c := fixedCheck{id: "fixed", results: []Result{{Check: "other", Status: OK, Code: "a"}, {Status: Warn, Code: "b"}}}
	r := Run(context.Background(), newTestEnv(t), []Check{c, c})
	if len(r.Results) != 4 {
		t.Fatalf("%d results", len(r.Results))
	}
	for _, res := range r.Results {
		if res.Check != "fixed" || res.Title != "fixed.title" {
			t.Errorf("result %+v", res)
		}
	}
}

type fixedCheck struct {
	id      string
	results []Result
}

func (c fixedCheck) ID() string                         { return c.id }
func (c fixedCheck) Title() string                      { return c.id + ".title" }
func (c fixedCheck) Run(context.Context, *Env) []Result { return append([]Result(nil), c.results...) }

// Two checks look at the tools; each tool is detected once.
func TestDetectionsAreShared(t *testing.T) {
	a := &fakeAdapter{id: "fake-a", detection: adapter.Detection{Status: adapter.NotInstalled}}
	env := newTestEnv(t)
	env.Adapters = []adapter.Adapter{a}
	Run(context.Background(), env, []Check{toolsCheck{}, toolRootsCheck{}})
	if a.calls != 1 {
		t.Errorf("Detect was called %d times", a.calls)
	}
}
