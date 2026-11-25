// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
)

// The workflow of the example repository from the person's side: turned on, synced (a slash
// command where the tool has those, a skill where it has not), then walked through with
// rgd flow next, which hands out a step only once the checks of the one before passed.
func TestAWorkflowFromSyncToTheLastStep(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the example's check runs ./gradlew through sh")
	}
	w := newWorld(t)
	initial := w.snapshot(false)
	const code = "change-to-merge"
	w.must(cliout.ExitOK, "enable", code, "--repo", w.repo)
	w.sync(cliout.ExitOK, "--yes-global")

	command := w.read("~/.fakecode/commands/" + brand.Prefix + code + ".md")
	skill := w.read("~/.otherai/skills/" + brand.Prefix + code + "/SKILL.md")
	for what, text := range map[string]string{"the command": command, "the skill": skill} {
		if !strings.Contains(text, brand.CLI+" flow next "+code) || strings.Contains(text, "gradlew") {
			t.Errorf("%s does not ask for the steps, or holds them:\n%s", what, text)
		}
	}

	// The project: its build passes or fails as the test says.
	project := t.TempDir()
	gradlew := filepath.Join(project, "gradlew")
	build := func(exit string) {
		if err := os.WriteFile(gradlew, []byte("#!/bin/sh\necho \"gradlew $*\"\nexit "+exit+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	build("1")
	next := func(code_ cliout.ExitCode, args ...string) result {
		t.Helper()
		return w.must(code_, append([]string{"flow", "next", code, "--dir", project}, args...)...)
	}

	r := next(cliout.ExitOK)
	inOrder(t, r.stdout, "从改动到合并", "step 1 of 2", brand.Prefix+"unit-testing", "Also use", brand.Prefix+"eng-handbook",
		"not installed on this computer", brand.CLI+" enable eng-handbook", "./gradlew test -q", brand.CLI+" flow next "+code)

	// Without evidence the step stands, and the build is not even run.
	r = next(cliout.ExitCheckFailed)
	inOrder(t, r.stdout, "No evidence given", "Not run yet")
	// With evidence, the build fails: the step still stands.
	r = next(cliout.ExitCheckFailed, "--evidence", "tests for the new rule in RuleTest")
	inOrder(t, r.stdout, "exited with 1", "gradlew test -q")
	build("0")
	r = next(cliout.ExitOK, "--evidence", "tests for the new rule in RuleTest")
	inOrder(t, r.stdout, "step 1 of 2", "✓", "step 2 of 2", brand.Prefix+"code-review", "Do not: 推送代码")

	// The last step needs the person.
	r = next(cliout.ExitCheckFailed, "--json", "--evidence", "every point has its reason")
	var doc struct {
		Schema  string
		Status  string
		Checked struct {
			Index  int
			Checks []struct {
				By     string
				Passed bool
				Reason string
			}
		}
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatalf("%v\n%s", err, r.stdout)
	}
	if doc.Schema != "rgd.flow/v1" || doc.Status != "failed" || doc.Checked.Index != 2 || len(doc.Checked.Checks) != 2 ||
		doc.Checked.Checks[1].By != "human" || doc.Checked.Checks[1].Reason != "confirmation" {
		t.Errorf("JSON %+v", doc)
	}
	r = next(cliout.ExitOK, "--evidence", "every point has its reason", "--confirmed")
	inOrder(t, r.stdout, "all 2 steps are done")

	// Another workflow code, and a code that is none.
	w.must(cliout.ExitFailure, "flow", "next", "no-such-flow", "--dir", project)
	w.must(cliout.ExitUsage, "flow", "next", "../etc", "--dir", project)

	w.must(cliout.ExitOK, "uninstall")
	if d := diffSnapshots(initial, w.snapshot(false)); len(d) > 0 {
		t.Errorf("after sync, flow next and uninstall the home directory differs:\n  %s", strings.Join(d, "\n  "))
	}
}

// A sync that changes a workflow mid-run stops the run from going on.
func TestAChangedWorkflowRestarts(t *testing.T) {
	w := newWorld(t)
	w.repoWrite("workflows/tidy.yaml", "meta: { name: Tidy }\nsteps:\n  - use: code-review\n    checks: [{ text: tidy, by: ai }]\n  - use: unit-testing\n")
	w.commit()
	w.sync(cliout.ExitOK, "--yes-global")
	project := t.TempDir()
	w.must(cliout.ExitOK, "flow", "next", "tidy", "--dir", project)

	w.repoWrite("workflows/tidy.yaml", "meta: { name: Tidy }\nsteps:\n  - use: unit-testing\n  - use: code-review\n")
	w.commit()
	w.sync(cliout.ExitOK, "--yes-global")
	r := w.must(cliout.ExitFailure, "flow", "next", "tidy", "--dir", project, "--evidence", "x")
	if !strings.Contains(r.stderr, "--restart") {
		t.Errorf("no way out offered:\n%s", r.stderr)
	}
	r = w.must(cliout.ExitOK, "flow", "next", "tidy", "--dir", project, "--restart")
	inOrder(t, r.stdout, "step 1 of 2", brand.Prefix+"unit-testing")
}
