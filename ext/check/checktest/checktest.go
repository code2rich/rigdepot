// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package checktest

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/check"
	engine "github.com/code2rich/rigdepot/internal/check"
	"github.com/code2rich/rigdepot/internal/check/repodir"
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

// Run runs one rule against the samples in dir, one subtest per sample, and fails the test when
// the rule does not behave:
//
//   - In every sample under hit/, the rule reports at least one finding.
//   - In every sample under miss/, it reports none.
//   - It finishes on every sample: no error, no panic, and every finding points at a file the
//     resource has, at a line inside it, with a message key that starts with "check.<rule ID>.".
//     The rule runs inside the real engine, so these are the engine's own checks.
//   - It reports the same findings when it runs a second time.
//   - Given a resource whose files cannot be read, it returns an error instead of reporting
//     nothing: "could not look" must never come back as "looked and found nothing". This is
//     tried on every resource the rule found something in under hit/.
//
// There must be at least one sample on each side. A sample is a directory laid out as a small
// resource repository (skills/<code>/…, mcp/<code>.yaml, …); every resource in it of a kind the
// rule applies to is checked, with the sample's other resources as its peers, and at least one
// must be of such a kind.
func Run(t *testing.T, r check.Rule, dir string) {
	t.Helper()
	samples, err := listSamples(dir)
	if err != nil {
		t.Fatalf("rule %q: %v", r.ID(), err)
	}
	for _, s := range samples {
		t.Run(s.name, func(t *testing.T) {
			for _, p := range runSample(r, s) {
				t.Error(p)
			}
		})
	}
}

type sample struct {
	name string // "hit/<directory>" or "miss/<directory>"
	dir  string
	hit  bool
}

func listSamples(dir string) ([]sample, error) {
	var out []sample
	for _, side := range []string{"hit", "miss"} {
		entries, err := os.ReadDir(filepath.Join(dir, side))
		if err != nil {
			return nil, fmt.Errorf("every rule needs samples under %s/hit and %s/miss: %w", dir, dir, err)
		}
		n := 0
		for _, e := range entries {
			if !e.IsDir() {
				continue // a README next to the samples
			}
			out = append(out, sample{name: side + "/" + e.Name(), dir: filepath.Join(dir, side, e.Name()), hit: side == "hit"})
			n++
		}
		if n == 0 {
			return nil, fmt.Errorf("there is no sample under %s/%s; every rule needs at least one that must be hit and one that looks similar and must not be", dir, side)
		}
	}
	return out, nil
}

// runSample returns what is wrong, one sentence each; nothing when the rule behaved.
func runSample(r check.Rule, s sample) []string {
	all, err := repodir.Load(os.DirFS(s.dir))
	if err != nil {
		return []string{fmt.Sprintf("reading the sample: %v", err)}
	}
	e, err := engine.New(check.Ruleset{Version: 1, Rules: []check.Rule{r}})
	if err != nil {
		return []string{err.Error()}
	}
	ctx := context.Background()
	var problems []string
	var hits []engine.Resource
	checked := 0
	for _, res := range all {
		if !r.Applies(res.Ref.Kind) {
			continue
		}
		checked++
		peers := engine.PeersOf(all, res.Ref)
		first, err := e.Check(ctx, res, peers)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", res.Ref, err))
			continue
		}
		for _, f := range first.Findings {
			if f.Message == engine.MsgRuleFailed {
				problems = append(problems, fmt.Sprintf("%s: the rule did not finish: %v", res.Ref, f.Params["error"]))
			}
		}
		second, err := e.Check(ctx, res, peers)
		if err == nil && !reflect.DeepEqual(first.Findings, second.Findings) {
			problems = append(problems, fmt.Sprintf("%s: a second run reported something else:\n%s\nthen\n%s", res.Ref, describe(first.Findings), describe(second.Findings)))
		}
		if !s.hit && len(first.Findings) > 0 {
			problems = append(problems, fmt.Sprintf("%s: this sample must not be hit, but the rule reported:\n%s", res.Ref, describe(first.Findings)))
		}
		if len(first.Findings) > 0 {
			hits = append(hits, res)
		}
	}
	if checked == 0 {
		return append(problems, "no resource in this sample is of a kind the rule applies to; a sample is laid out as a repository: skills/<code>/SKILL.md, mcp/<code>.yaml, …")
	}
	if s.hit && len(hits) == 0 {
		problems = append(problems, "this sample must be hit, but the rule reported nothing")
	}
	for _, res := range hits {
		in, err := engine.NewInput(res, engine.PeersOf(all, res.Ref))
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		in.Files = unreadable{}
		if found, err := callRule(ctx, r, in); err == nil {
			problems = append(problems, fmt.Sprintf("%s: when its files cannot be read, the rule returns no error (and %d findings); it must say that it could not look", res.Ref, len(found)))
		}
	}
	return problems
}

func callRule(ctx context.Context, r check.Rule, in check.Input) (found []check.Finding, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return r.Check(ctx, in)
}

func describe(found []check.Finding) string {
	var b strings.Builder
	for _, f := range found {
		fmt.Fprintf(&b, "  %s:%d %s %s", f.File, f.Line, f.Severity, f.Message)
		if len(f.Params) > 0 {
			keys := make([]string, 0, len(f.Params))
			for k := range f.Params {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&b, " %s=%v", k, f.Params[k])
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// unreadable is a file system on which nothing can be read.
type unreadable struct{}

func (unreadable) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
}
