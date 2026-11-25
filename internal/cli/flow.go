// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/flow"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
)

func init() {
	register("", newFlowCmd)
	register("flow", newFlowNextCmd)
}

// newFlowCmd: rgd flow, the workflows synced to this computer.
func newFlowCmd(a *app) *cobra.Command {
	return a.group("flow", a.p.T(msg.FlowShort))
}

// flowFlags are the options of rgd flow next.
type flowFlags struct {
	evidence  string
	confirmed bool
	restart   bool
	dir       string
}

// checkTimeout is how long a check that is a command may run.
const checkTimeout = 30 * time.Minute

// newFlowNextCmd: rgd flow next <workflow>. The AI runs it at the start of a workflow and after
// each step: it checks the step handed out last and hands out the next one only when every check
// has passed. Without a run under way it hands out the first step.
//
// Exit codes: 0 a step was handed out, or the workflow is done; 3 the checks passed and the step
// waits for the person (--confirmed); 4 a check did not pass; 1 no such workflow on this
// computer, or it changed since the run started; 9 the run's file cannot be read.
func newFlowNextCmd(a *app) *cobra.Command {
	var f flowFlags
	cmd := &cobra.Command{
		Use:   "next <workflow>",
		Short: a.p.T(msg.FlowNextShort),
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runFlowNext(cmd, args[0], f)
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&f.evidence, "evidence", "", a.p.T(msg.FlagFlowEvidence))
	fs.BoolVar(&f.confirmed, "confirmed", false, a.p.T(msg.FlagFlowConfirmed))
	fs.BoolVar(&f.restart, "restart", false, a.p.T(msg.FlagFlowRestart))
	fs.StringVar(&f.dir, "dir", "", a.p.T(msg.FlagFlowDir))
	return cmd
}

func (a *app) runFlowNext(cmd *cobra.Command, code string, f flowFlags) error {
	if !resource.ValidCode(code) {
		return a.usageError("bad_workflow", a.p.T(msg.FlowBadCode, i18n.V("workflow", code)), cmd.CommandPath())
	}
	if a.home == "" {
		return a.noHomeError()
	}
	dataPath, err := filepath.Abs(a.home)
	if err != nil {
		return a.noHomeError()
	}
	data, err := home.At(dataPath)
	if err != nil {
		return a.noHomeError()
	}
	dir := f.dir
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return a.usageError("bad_dir", a.p.T(msg.FlowBadDir, i18n.V("dir", dir)), cmd.CommandPath())
	}

	res, err := flow.Next(cmd.Context(), data, flow.Input{
		Code: code, Dir: dir, Evidence: f.evidence, Confirmed: f.confirmed, Restart: f.restart,
	}, flow.Shell{Timeout: checkTimeout}, time.Now())
	next := brand.CLI + " flow next " + code
	switch {
	case errors.Is(err, flow.ErrUnknownFlow):
		e := cliout.Fail(cliout.ExitFailure, "flow_unknown", a.p.T(msg.FlowUnknown, i18n.V("workflow", code)))
		e.Hint = a.p.T(msg.FlowUnknownHint, i18n.V("command", brand.CLI+" sync"))
		e.Err = err
		return e
	case errors.Is(err, flow.ErrChanged):
		e := cliout.Fail(cliout.ExitFailure, "flow_changed", a.p.T(msg.FlowChanged, i18n.V("workflow", code), i18n.V("command", next+" --restart")))
		e.Err = err
		return e
	case errors.Is(err, flow.ErrUnreadable):
		e := cliout.Fail(cliout.ExitStateUnknown, "flow_unreadable", a.p.T(msg.FlowUnreadable, i18n.V("detail", err.Error()), i18n.V("command", next+" --restart")))
		e.Err = err
		return e
	case err != nil:
		return err
	}

	doc := newFlowDoc(res, next)
	if err := a.out.Result(cliout.Schema("flow", 1), doc, func(w io.Writer) error {
		return a.writeFlow(w, res, next)
	}); err != nil {
		return err
	}
	switch res.Status {
	case flow.Failed:
		return cliout.Status(cliout.ExitCheckFailed)
	case flow.Waiting:
		return cliout.Status(cliout.ExitPending)
	}
	return nil
}

// flowDoc is the JSON output of rgd flow next (schema rgd.flow/v1).
type flowDoc struct {
	Workflow flowWorkflowDoc `json:"workflow"`
	// Status is started, advanced, failed, waiting or done.
	Status string `json:"status"`
	// Checked is the step whose checks were run, for every status but started.
	Checked *flowCheckedDoc `json:"checked,omitempty"`
	// Step is the step handed out, for started and advanced.
	Step *flowStepDoc `json:"step,omitempty"`
	// Next is the command to run when the step is done.
	Next string `json:"next,omitempty"`
}

type flowWorkflowDoc struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Steps int    `json:"steps"`
}

type flowCheckedDoc struct {
	// Index counts from 1.
	Index  int            `json:"index"`
	Checks []flowCheckDoc `json:"checks"`
}

type flowCheckDoc struct {
	Text     string `json:"text"`
	By       string `json:"by"`
	Run      string `json:"run,omitempty"`
	Passed   *bool  `json:"passed,omitempty"`
	Reason   string `json:"reason,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"`
	Output   string `json:"output,omitempty"`
}

type flowStepDoc struct {
	// Index counts from 1.
	Index  int            `json:"index"`
	Use    flowRefDoc     `json:"use"`
	With   []flowRefDoc   `json:"with"`
	Forbid []string       `json:"forbid"`
	Then   string         `json:"then"`
	Checks []flowCheckDoc `json:"checks"`
}

type flowRefDoc struct {
	Code string `json:"code"`
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
	// InTool is the name the resource has in the AI tools.
	InTool    string `json:"inTool"`
	Delivered bool   `json:"delivered"`
}

func newFlowDoc(res *flow.Result, next string) flowDoc {
	d := flowDoc{
		Workflow: flowWorkflowDoc{Code: res.Def.Code, Name: res.Def.Name, Steps: len(res.Def.Steps)},
		Status:   string(res.Status),
	}
	if res.Status != flow.Started {
		idx := res.Step
		if res.Status == flow.Advanced {
			idx = res.Previous
		}
		c := &flowCheckedDoc{Index: idx + 1, Checks: []flowCheckDoc{}}
		for _, r := range res.Checks {
			passed := r.Passed
			cd := flowCheckDoc{Text: r.Text, By: r.By, Run: r.Run, Passed: &passed, Reason: r.Reason, Output: r.Output}
			if r.By == kind.CheckByCommand && r.Reason != flow.ReasonNotRun {
				exit := r.ExitCode
				cd.ExitCode = &exit
			}
			c.Checks = append(c.Checks, cd)
		}
		d.Checked = c
	}
	if res.Status == flow.Started || res.Status == flow.Advanced {
		st := res.Def.Steps[res.Step]
		sd := &flowStepDoc{Index: res.Step + 1, Use: refDoc(st.Use), With: []flowRefDoc{}, Forbid: st.Forbid, Then: st.Then, Checks: []flowCheckDoc{}}
		if sd.Forbid == nil {
			sd.Forbid = []string{}
		}
		for _, w := range st.With {
			sd.With = append(sd.With, refDoc(w))
		}
		for _, c := range st.Checks {
			sd.Checks = append(sd.Checks, flowCheckDoc{Text: c.Text, By: c.By, Run: c.Run})
		}
		d.Step = sd
	}
	if res.Status != flow.Done {
		d.Next = next
	}
	return d
}

func refDoc(r flow.Ref) flowRefDoc {
	return flowRefDoc{Code: r.Code, Kind: r.Kind, Name: r.Name, InTool: brand.Prefix + r.Code, Delivered: r.Delivered}
}

// writeFlow is the human output of rgd flow next. The AI reads it, so it says plainly what to do
// next.
func (a *app) writeFlow(w io.Writer, res *flow.Result, next string) error {
	def := res.Def
	var lines []string
	add := func(s string) { lines = append(lines, s) }
	total := i18n.V("total", len(def.Steps))
	name := i18n.V("workflow", def.Name)

	if res.Status != flow.Started {
		checked := res.Step
		if res.Status == flow.Advanced {
			checked = res.Previous
		}
		add(a.p.T(msg.FlowCheckedHead, name, i18n.V("step", checked+1), total))
		for _, c := range res.Checks {
			mark, note := a.out.Paint(cliout.Green, "✓"), ""
			if !c.Passed {
				mark = a.out.Paint(cliout.Red, "✗")
				note = a.checkReason(c, next)
			}
			add("  " + mark + " " + c.Text + a.out.Paint(cliout.Dim, " · "+a.checkBy(c.Check)))
			if note != "" {
				add("      " + note)
			}
			if !c.Passed && c.Output != "" {
				for _, ln := range lastLines(c.Output, 10) {
					add("      " + a.out.Paint(cliout.Dim, ln))
				}
			}
		}
	}
	switch res.Status {
	case flow.Failed:
		add("")
		add(a.p.T(msg.FlowFailed, i18n.V("command", next)))
	case flow.Waiting:
		add("")
		add(a.p.T(msg.FlowWaiting, i18n.V("command", next+" --confirmed")))
	case flow.Done:
		add("")
		add(a.p.T(msg.FlowDone, name, total))
	case flow.Started, flow.Advanced:
		if res.Status == flow.Advanced {
			add("")
		}
		st := def.Steps[res.Step]
		add(a.out.Paint(cliout.Bold, a.p.T(msg.FlowStepHead, name, i18n.V("step", res.Step+1), total)))
		add("  " + a.p.T(msg.FlowStepUse, i18n.V("what", a.refText(st.Use))))
		for _, r := range st.With {
			add("  " + a.p.T(msg.FlowStepWith, i18n.V("what", a.refText(r))))
		}
		for _, x := range st.Forbid {
			add("  " + a.p.T(msg.FlowStepForbid, i18n.V("what", x)))
		}
		if len(st.Checks) > 0 {
			add("  " + a.p.T(msg.FlowStepChecks))
			for _, c := range st.Checks {
				add("    - " + c.Text + a.out.Paint(cliout.Dim, " · "+a.checkBy(c)))
			}
		}
		if st.Then == kind.ThenConfirm && res.Step < len(def.Steps)-1 {
			add("  " + a.p.T(msg.FlowStepConfirm))
		}
		add("")
		add(a.p.T(msg.FlowStepNext, i18n.V("command", next)))
	}
	for _, ln := range lines {
		if _, err := fmt.Fprintln(w, strings.TrimRight("  "+ln, " ")); err != nil {
			return err
		}
	}
	return nil
}

// refText says what a step uses, the way the AI finds it in its tool.
func (a *app) refText(r flow.Ref) string {
	inTool := i18n.V("name", brand.Prefix+r.Code)
	title := i18n.V("title", r.Name)
	var s string
	switch r.Kind {
	case resource.KindSkill:
		s = a.p.T(msg.FlowRefSkill, inTool, title)
	case resource.KindAgent:
		s = a.p.T(msg.FlowRefAgent, inTool, title)
	case resource.KindKnowledge:
		s = a.p.T(msg.FlowRefKnowledge, inTool, title)
	case resource.KindMCP:
		s = a.p.T(msg.FlowRefMCP, i18n.V("name", r.Code), title)
	case "":
		return a.p.T(msg.FlowRefMissing, i18n.V("name", r.Code))
	default:
		s = a.p.T(msg.FlowRefOther, inTool, title)
	}
	if !r.Delivered {
		s += " " + a.p.T(msg.FlowRefNotHere, i18n.V("command", brand.CLI+" enable "+r.Code))
	}
	return s
}

// checkBy says who makes a check and how it is passed.
func (a *app) checkBy(c flow.Check) string {
	switch c.By {
	case kind.CheckByAI:
		return a.p.T(msg.FlowByAi)
	case kind.CheckByHuman:
		return a.p.T(msg.FlowByHuman)
	case kind.CheckByCommand:
		return a.p.T(msg.FlowByCommand, i18n.V("run", c.Run))
	}
	return c.By
}

// checkReason says why a check did not pass and what to do about it.
func (a *app) checkReason(c flow.CheckResult, next string) string {
	switch c.Reason {
	case flow.ReasonEvidence:
		return a.p.T(msg.FlowReasonEvidence, i18n.V("command", next+` --evidence "…"`))
	case flow.ReasonConfirmation:
		return a.p.T(msg.FlowReasonConfirmation, i18n.V("command", next+" --confirmed"))
	case flow.ReasonExit:
		return a.p.T(msg.FlowReasonExit, i18n.V("code", c.ExitCode))
	case flow.ReasonNotRun:
		return a.p.T(msg.FlowReasonNotRun)
	}
	return a.p.T(msg.FlowReasonError)
}

// lastLines returns at most n lines from the end of s.
func lastLines(s string, n int) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
