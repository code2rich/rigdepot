// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package checktest

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// The structure check looks for testenv.Main in TestMain; Main is the same call.
func TestMain(m *testing.M) { testenv.Main(m) }

// flagRule reports every file named FLAG in a skill. Its fields break it in one way each.
type flagRule struct {
	kind        string // the kind it applies to; skill when empty
	never       bool   // reports nothing
	always      bool   // reports every file
	fail        bool   // returns an error
	badLine     bool   // reports a line past the end of the file
	ignoreFiles bool   // reports from the metadata alone, without reading
	calls       *int   // reports a different file on every second call
}

func (flagRule) ID() string               { return "flag" }
func (flagRule) Severity() check.Severity { return check.Advisory }
func (r flagRule) Applies(kind string) bool {
	if r.kind != "" {
		return kind == r.kind
	}
	return kind == resource.KindSkill
}

func (r flagRule) Check(_ context.Context, in check.Input) ([]check.Finding, error) {
	if r.fail {
		return nil, errors.New("cannot")
	}
	if r.ignoreFiles {
		return []check.Finding{{Message: "check.flag.meta"}}, nil
	}
	var found []check.Finding
	err := fs.WalkDir(in.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || r.never || (!r.always && path.Base(p) != "FLAG") {
			return nil
		}
		line := 1
		if r.badLine {
			line = 99
		}
		found = append(found, check.Finding{File: p, Line: line, Message: "check.flag.found"})
		return nil
	})
	if r.calls != nil {
		*r.calls++
		if *r.calls%2 == 0 {
			found = append(found, check.Finding{File: "SKILL.md", Message: "check.flag.extra"})
		}
	}
	return found, err
}

// problems runs the rule against the samples in dir and returns what Run would report, by sample.
func problems(t *testing.T, r check.Rule, dir string) map[string][]string {
	t.Helper()
	samples, err := listSamples(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, s := range samples {
		if p := runSample(r, s); len(p) > 0 {
			out[s.name] = p
		}
	}
	return out
}

func TestRun(t *testing.T) {
	Run(t, flagRule{}, "testdata/flag")
}

func TestAWorkingRuleHasNoProblems(t *testing.T) {
	if got := problems(t, flagRule{}, "testdata/flag"); len(got) != 0 {
		t.Errorf("problems = %v", got)
	}
}

// Each way a rule can misbehave is reported on the samples where it shows, and says what it is.
func TestABrokenRuleIsCaught(t *testing.T) {
	calls := 0
	cases := []struct {
		name   string
		rule   flagRule
		sample string
		want   string
	}{
		{"misses a hit", flagRule{never: true}, "hit/flagged", "must be hit"},
		{"hits a miss", flagRule{always: true}, "miss/lookalike", "must not be hit"},
		{"returns an error", flagRule{fail: true}, "miss/lookalike", "did not finish"},
		{"reports a line past the end", flagRule{badLine: true}, "hit/flagged", "did not finish"},
		{"does not notice unreadable files", flagRule{ignoreFiles: true}, "hit/flagged", "cannot be read"},
		{"changes its mind", flagRule{calls: &calls}, "hit/flagged", "second run"},
		{"applies to nothing in the samples", flagRule{kind: resource.KindAgent}, "hit/flagged", "no resource in this sample"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := problems(t, c.rule, "testdata/flag")[c.sample]
			if !strings.Contains(strings.Join(got, "\n"), c.want) {
				t.Errorf("problems with %s = %q, want one saying %q", c.sample, got, c.want)
			}
		})
	}
}

func TestSamplesOnBothSidesAreRequired(t *testing.T) {
	for _, dir := range []string{"testdata/only-hit", "testdata/none"} {
		if _, err := listSamples(dir); err == nil {
			t.Errorf("%s: a rule without samples on both sides was accepted", dir)
		}
	}
}
