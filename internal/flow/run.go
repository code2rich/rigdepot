// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/internal/home"
)

// Run is a workflow under way in one directory.
type Run struct {
	Schema int    `json:"schema"`
	Code   string `json:"code"`
	// Dir is the directory the run belongs to; the checks that are commands run there.
	Dir string `json:"dir"`
	// Digest is the workflow's digest when the run started.
	Digest string `json:"digest"`
	// Step is the index of the step handed out last, from 0.
	Step    int       `json:"step"`
	Started time.Time `json:"started"`
	// Passed records the steps whose checks passed, in order.
	Passed []Passed `json:"passed,omitempty"`
}

// Passed is a step whose checks passed: when, and on what word.
type Passed struct {
	Step      int       `json:"step"`
	At        time.Time `json:"at"`
	Evidence  string    `json:"evidence,omitempty"`
	Confirmed bool      `json:"confirmed,omitempty"`
}

// Status says what Next did.
type Status string

const (
	// Started: there was no run; the first step is handed out.
	Started Status = "started"
	// Advanced: the checks of the step before passed; the next step is handed out.
	Advanced Status = "advanced"
	// Failed: a check did not pass; nothing moved. The same step stands.
	Failed Status = "failed"
	// Waiting: the checks passed and the step waits for the person to say to go on.
	Waiting Status = "waiting"
	// Done: the last step's checks passed; the run is over.
	Done Status = "done"
)

// Input is what the AI says along with asking for the next step.
type Input struct {
	// Code is the workflow; Dir the directory of the project, absolute.
	Code, Dir string
	// Evidence is the AI's evidence for the checks it makes.
	Evidence string
	// Confirmed says the person confirmed the checks they make and agreed to go on.
	Confirmed bool
	// Restart throws away the run under way and starts with the first step.
	Restart bool
}

// Result is what Next did.
type Result struct {
	Status Status
	Def    *Definition
	Run    *Run
	// Step is the index of the step handed out (Started, Advanced) or the one that was checked
	// (Failed, Waiting, Done).
	Step int
	// Checks are the results of the checks of the step that was checked; empty for Started.
	Checks []CheckResult
	// Previous is the step that passed before the one handed out, for Advanced.
	Previous int
}

// CheckResult is one check of a step, as it went.
type CheckResult struct {
	Check
	Passed bool
	// Reason says why it did not pass: "evidence", "confirmation", "exit" (the command exited
	// with ExitCode), "error" (it could not be run: Output says why) or "not_run" (other checks
	// have to pass first).
	Reason   string
	ExitCode int
	// Output is the end of what a command printed.
	Output string
}

// The values of CheckResult.Reason.
const (
	ReasonEvidence     = "evidence"
	ReasonConfirmation = "confirmation"
	ReasonExit         = "exit"
	ReasonError        = "error"
	ReasonNotRun       = "not_run"
)

// Runner runs the command of a check in a directory and returns its exit code and output.
type Runner interface {
	Run(ctx context.Context, dir, command string) (exit int, output []byte, err error)
}

// Shell runs commands with the system's shell: sh -c on macOS and Linux, cmd /C on Windows, with
// a time limit.
type Shell struct {
	Timeout time.Duration
}

// Run implements Runner. A command that runs over the time limit is stopped and counts as
// failed.
func (s Shell) Run(ctx context.Context, dir, command string) (int, []byte, error) {
	if s.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Timeout)
		defer cancel()
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, out.Bytes(), nil
	case ctx.Err() != nil:
		return -1, out.Bytes(), fmt.Errorf("stopped after %s", s.Timeout)
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), out.Bytes(), nil
	}
	return -1, out.Bytes(), err
}

// maxOutput is how much of the end of a command's output a result keeps.
const maxOutput = 4000

// Next is rgd flow next: it checks the step handed out last and hands out the next one, or starts
// a run. See the package documentation.
func Next(ctx context.Context, data home.Dir, in Input, r Runner, now time.Time) (*Result, error) {
	def, err := Load(data, in.Code)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(in.Dir) {
		return nil, fmt.Errorf("flow: the directory %q is not absolute", in.Dir)
	}
	file := runFile(data, in.Code, in.Dir)
	run, err := readRun(file)
	if err != nil {
		return nil, err
	}
	if run == nil || in.Restart {
		run = &Run{Schema: formatVersion, Code: def.Code, Dir: filepath.Clean(in.Dir), Digest: def.Digest, Started: now.UTC()}
		if err := writeRun(data, file, run); err != nil {
			return nil, err
		}
		return &Result{Status: Started, Def: def, Run: run, Step: 0}, nil
	}
	if run.Digest != def.Digest || run.Step >= len(def.Steps) {
		return nil, fmt.Errorf("%w: %s", ErrChanged, def.Code)
	}

	step := def.Steps[run.Step]
	res := &Result{Def: def, Run: run, Step: run.Step}
	res.Checks = check(ctx, step, in, r)
	for _, c := range res.Checks {
		if !c.Passed {
			res.Status = Failed
			return res, nil
		}
	}
	if step.Then == kind.ThenConfirm && !in.Confirmed && run.Step < len(def.Steps)-1 {
		res.Status = Waiting
		return res, nil
	}
	run.Passed = append(run.Passed, Passed{Step: run.Step, At: now.UTC(), Evidence: in.Evidence, Confirmed: in.Confirmed})
	if run.Step == len(def.Steps)-1 {
		res.Status = Done
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return res, nil
	}
	res.Previous = run.Step
	run.Step++
	res.Step = run.Step
	res.Status = Advanced
	if err := writeRun(data, file, run); err != nil {
		return nil, err
	}
	return res, nil
}

// check runs the checks of a step: the AI's and the person's first, the commands only when those
// passed.
func check(ctx context.Context, step Step, in Input, r Runner) []CheckResult {
	out := make([]CheckResult, len(step.Checks))
	ok := true
	for i, c := range step.Checks {
		out[i] = CheckResult{Check: c, Passed: true}
		switch c.By {
		case kind.CheckByAI:
			if strings.TrimSpace(in.Evidence) == "" {
				out[i].Passed, out[i].Reason = false, ReasonEvidence
			}
		case kind.CheckByHuman:
			if !in.Confirmed {
				out[i].Passed, out[i].Reason = false, ReasonConfirmation
			}
		case kind.CheckByCommand:
			continue
		default:
			// A kind of check this program does not know cannot be said to have passed.
			out[i].Passed, out[i].Reason = false, ReasonError
			out[i].Output = fmt.Sprintf("this version cannot make a check by %q", c.By)
		}
		ok = ok && out[i].Passed
	}
	for i, c := range step.Checks {
		if c.By != kind.CheckByCommand {
			continue
		}
		if !ok {
			out[i].Passed, out[i].Reason = false, ReasonNotRun
			continue
		}
		exit, output, err := r.Run(ctx, in.Dir, c.Run)
		out[i].ExitCode, out[i].Output = exit, tail(output)
		switch {
		case err != nil:
			out[i].Passed, out[i].Reason = false, ReasonError
			out[i].Output = strings.TrimSpace(out[i].Output + "\n" + err.Error())
		case exit != 0:
			out[i].Passed, out[i].Reason = false, ReasonExit
		}
	}
	return out
}

// tail returns the end of a command's output, at most maxOutput bytes, starting at a line.
func tail(b []byte) string {
	if len(b) > maxOutput {
		b = b[len(b)-maxOutput:]
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return strings.TrimRight(string(b), "\n")
}

// readRun reads a run; nil when there is none.
func readRun(file string) (*Run, error) {
	raw, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrUnreadable, file, err)
	}
	var run Run
	if err := json.Unmarshal(raw, &run); err != nil || run.Schema < 1 || run.Schema > formatVersion {
		return nil, fmt.Errorf("%w: %s: not a run this program can read", ErrUnreadable, file)
	}
	return &run, nil
}

func writeRun(data home.Dir, file string, run *Run) error {
	if _, err := data.EnsureSub(home.FlowsDir + "/runs"); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	return home.WriteFileAtomic(file, append(raw, '\n'), home.FilePerm)
}
