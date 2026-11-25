// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package flow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

var (
	ctx = context.Background()
	now = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
)

// flowSpec is a workflow as a kind hands it over.
type flowSpec struct{ steps []kind.Step }

func (f flowSpec) Steps() []kind.Step { return f.steps }

var taskToSubmit = flowSpec{steps: []kind.Step{
	{Use: "task-development", With: []string{"eng-handbook"}, Then: kind.ThenContinue, Checks: []kind.Check{
		{Text: "stays within the task", By: kind.CheckByAI},
		{Text: "compiles", By: kind.CheckByCommand, Run: "make build"},
	}},
	{Use: "unit-testing", Then: kind.ThenConfirm, Checks: []kind.Check{
		{Text: "tests pass", By: kind.CheckByCommand, Run: "make test"},
	}},
	{Use: "delivery-check", Then: kind.ThenContinue, Forbid: []string{"push"}, Checks: []kind.Check{
		{Text: "the person agrees", By: kind.CheckByHuman},
	}},
}}

func state(digest string) *desired.State {
	return &desired.State{
		Resources: []desired.Resource{
			{Ref: resource.Ref{Kind: resource.KindSkill, Code: "task-development"}, Meta: resource.Meta{Name: "Task development"}},
			{Ref: resource.Ref{Kind: resource.KindWorkflow, Code: "task-to-submit"}, Meta: resource.Meta{Name: "From task to test"}, Digest: resource.Digest(digest), Spec: taskToSubmit},
			{Ref: resource.Ref{Kind: resource.KindSkill, Code: "unit-testing"}, Meta: resource.Meta{Name: "Unit testing"}},
		},
		Excluded: []desired.Excluded{
			{Ref: resource.Ref{Kind: resource.KindKnowledge, Code: "eng-handbook"}, Meta: resource.Meta{Name: "Handbook"}, Reason: desired.ExcludedNotEnabled},
		},
	}
}

// commands is a Runner that answers from a table and records what it ran, and where.
type commands struct {
	exit map[string]int
	ran  []string
}

func (c *commands) Run(_ context.Context, dir, command string) (int, []byte, error) {
	c.ran = append(c.ran, command+" in "+filepath.Base(dir))
	return c.exit[command], []byte("output of " + command + "\n"), nil
}

func setup(t *testing.T) (home.Dir, string) {
	t.Helper()
	data, err := home.At(filepath.Join(t.TempDir(), ".data"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Keep(data, state("sha256:1")); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(t.TempDir(), "shop")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	return data, project
}

func next(t *testing.T, data home.Dir, in Input, r Runner) *Result {
	t.Helper()
	if in.Code == "" {
		in.Code = "task-to-submit"
	}
	res, err := Next(ctx, data, in, r, now)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// What a sync keeps: the steps, with the kind and name of what they use and whether this computer
// has it.
func TestKeepWritesWhatFlowNextNeeds(t *testing.T) {
	data, _ := setup(t)
	def, err := Load(data, "task-to-submit")
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "From task to test" || def.Digest != "sha256:1" || len(def.Steps) != 3 {
		t.Fatalf("definition %+v", def)
	}
	s := def.Steps[0]
	if s.Use != (Ref{Code: "task-development", Kind: resource.KindSkill, Name: "Task development", Delivered: true}) {
		t.Errorf("use %+v", s.Use)
	}
	if len(s.With) != 1 || s.With[0] != (Ref{Code: "eng-handbook", Kind: resource.KindKnowledge, Name: "Handbook"}) {
		t.Errorf("with %+v: known, but not delivered", s.With)
	}
	if def.Steps[2].Use != (Ref{Code: "delivery-check"}) {
		t.Errorf("a resource the repository does not have: %+v", def.Steps[2].Use)
	}

	// A workflow the next state does not deliver goes; one that could not be read stays.
	if err := os.WriteFile(data.Join(home.FlowsDir, "old-one.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &desired.State{Failed: []desired.Failed{{Ref: resource.Ref{Kind: resource.KindWorkflow, Code: "task-to-submit"}}}}
	kept, err := Keep(data, st)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kept.Removed, []string{"old-one"}) || len(kept.Written) != 0 {
		t.Errorf("kept %+v", kept)
	}
	if _, err := Load(data, "task-to-submit"); err != nil {
		t.Errorf("the workflow that could not be read was removed: %v", err)
	}
}

// The whole way through: each step only once the checks of the one before passed.
func TestAStepIsHandedOutOnlyOnceTheChecksBeforeItPassed(t *testing.T) {
	data, project := setup(t)
	r := &commands{exit: map[string]int{"make build": 0, "make test": 1}}

	res := next(t, data, Input{Dir: project}, r)
	if res.Status != Started || res.Step != 0 || len(r.ran) != 0 {
		t.Fatalf("first call: %+v, ran %v", res, r.ran)
	}

	// No evidence: the command is not even run.
	res = next(t, data, Input{Dir: project}, r)
	if res.Status != Failed || res.Step != 0 || res.Checks[0].Reason != ReasonEvidence || res.Checks[1].Reason != ReasonNotRun || len(r.ran) != 0 {
		t.Fatalf("without evidence: %+v, ran %v", res, r.ran)
	}

	res = next(t, data, Input{Dir: project, Evidence: "only the files of the task changed"}, r)
	if res.Status != Advanced || res.Step != 1 || res.Previous != 0 || !slices.Equal(r.ran, []string{"make build in shop"}) {
		t.Fatalf("with evidence: %+v, ran %v", res, r.ran)
	}

	// The tests fail: the step stands.
	res = next(t, data, Input{Dir: project}, r)
	if res.Status != Failed || res.Step != 1 || res.Checks[0].Reason != ReasonExit || res.Checks[0].ExitCode != 1 || res.Checks[0].Output != "output of make test" {
		t.Fatalf("failing tests: %+v", res)
	}
	r.exit["make test"] = 0
	// They pass, and the step says to wait for the person.
	res = next(t, data, Input{Dir: project}, r)
	if res.Status != Waiting || res.Step != 1 {
		t.Fatalf("passing tests, not confirmed: %+v", res)
	}
	res = next(t, data, Input{Dir: project, Confirmed: true}, r)
	if res.Status != Advanced || res.Step != 2 {
		t.Fatalf("confirmed: %+v", res)
	}
	// The last step: the person's check.
	if res = next(t, data, Input{Dir: project}, r); res.Status != Failed || res.Checks[0].Reason != ReasonConfirmation {
		t.Fatalf("last step, not confirmed: %+v", res)
	}
	if res = next(t, data, Input{Dir: project, Confirmed: true}, r); res.Status != Done {
		t.Fatalf("last step, confirmed: %+v", res)
	}
	if len(res.Run.Passed) != 3 || res.Run.Passed[0].Evidence != "only the files of the task changed" {
		t.Errorf("passed %+v", res.Run.Passed)
	}
	// Over: the next call starts again.
	if res = next(t, data, Input{Dir: project}, r); res.Status != Started {
		t.Fatalf("after the end: %+v", res)
	}
}

// A run belongs to its directory: the same workflow in another project starts on its own.
func TestRunsAreByDirectory(t *testing.T) {
	data, project := setup(t)
	r := &commands{exit: map[string]int{}}
	next(t, data, Input{Dir: project}, r)
	next(t, data, Input{Dir: project, Evidence: "yes"}, r)
	other := filepath.Join(filepath.Dir(project), "pay")
	if res := next(t, data, Input{Dir: other}, r); res.Status != Started {
		t.Errorf("another directory: %+v", res)
	}
	if res := next(t, data, Input{Dir: project, Restart: true}, r); res.Status != Started || res.Step != 0 {
		t.Errorf("restart: %+v", res)
	}
}

// A sync that changes the workflow mid-run does not let the run go on from a step that may mean
// something else; a run file that cannot be read is unknown, not "no run".
func TestChangedAndUnreadable(t *testing.T) {
	data, project := setup(t)
	r := &commands{exit: map[string]int{}}
	next(t, data, Input{Dir: project}, r)
	if _, err := Keep(data, state("sha256:2")); err != nil {
		t.Fatal(err)
	}
	if _, err := Next(ctx, data, Input{Code: "task-to-submit", Dir: project, Evidence: "x"}, r, now); !errors.Is(err, ErrChanged) {
		t.Errorf("after a sync changed it: %v", err)
	}
	if res := next(t, data, Input{Dir: project, Restart: true}, r); res.Status != Started {
		t.Errorf("restart after a change: %+v", res)
	}

	if err := os.WriteFile(runFile(data, "task-to-submit", project), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Next(ctx, data, Input{Code: "task-to-submit", Dir: project}, r, now); !errors.Is(err, ErrUnreadable) {
		t.Errorf("unreadable run: %v", err)
	}
	if _, err := Next(ctx, data, Input{Code: "nothing-here", Dir: project}, r, now); !errors.Is(err, ErrUnknownFlow) {
		t.Errorf("unknown workflow: %v", err)
	}
	if _, err := Next(ctx, data, Input{Code: "../etc", Dir: project}, r, now); !errors.Is(err, ErrUnknownFlow) {
		t.Errorf("a code that is not one: %v", err)
	}
}

// The shell runs the command in the directory, and its exit code decides.
func TestShell(t *testing.T) {
	dir := t.TempDir()
	pass, fail := "echo here > seen.txt", "exit 3"
	if runtime.GOOS == "windows" {
		pass = "echo here> seen.txt"
	}
	exit, _, err := Shell{Timeout: time.Minute}.Run(ctx, dir, pass)
	if err != nil || exit != 0 {
		t.Fatalf("%q: %d, %v", pass, exit, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "seen.txt")); err != nil {
		t.Errorf("the command did not run in the directory: %v", err)
	}
	if exit, _, err := (Shell{Timeout: time.Minute}).Run(ctx, dir, fail); err != nil || exit != 3 {
		t.Errorf("%q: %d, %v", fail, exit, err)
	}
	if runtime.GOOS != "windows" {
		if _, out, err := (Shell{Timeout: 100 * time.Millisecond}).Run(ctx, dir, "sleep 5"); err == nil || !strings.Contains(err.Error(), "stopped") {
			t.Errorf("a command over time: %v, %s", err, out)
		}
	}
}

func TestTailKeepsTheEnd(t *testing.T) {
	long := strings.Repeat("line\n", 2000) + "the end\n"
	got := tail([]byte(long))
	if len(got) > maxOutput || !strings.HasSuffix(got, "the end") || strings.HasPrefix(got, "ine") {
		t.Errorf("tail: %d bytes, ends %q", len(got), got[len(got)-10:])
	}
}
