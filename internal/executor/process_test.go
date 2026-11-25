// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package executor

// A write killed halfway is the one failure the executor cannot clean up after itself. These
// tests start this test binary as a separate process, stop it dead at a step of a directory
// replacement, and check that the next run puts things right.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/brand"
)

var (
	helperVar = brand.EnvPrefix + "TEST_EXECUTOR_HELPER"
	homeVar   = brand.EnvPrefix + "TEST_EXECUTOR_HOME"
	opVar     = brand.EnvPrefix + "TEST_EXECUTOR_OP" // "key": set a key instead
)

// TestHelperProcess is not a test: it is what the helper runs. It replaces skill v1 with v2, or
// sets a key in ~/.claude.json, and stops dead at the step named by the variable.
func TestHelperProcess(t *testing.T) {
	stopAt := os.Getenv(helperVar)
	if stopAt == "" {
		return
	}
	e := openEnv(t, os.Getenv(homeVar))
	e.ex.hook = func(step string) error {
		if step == stopAt {
			fmt.Println("STOPPED")
			time.Sleep(time.Hour) // killed here
		}
		return nil
	}
	var op adapter.Op = skill("v2")
	tool := claude
	if os.Getenv(opVar) == "key" {
		op, tool = claudeOp(jiraServer()), claudeKeys
	}
	if _, err := e.ex.Apply(ctx, tool, op, e.ex.Observe(TargetOf(op)), Options{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(3) // the step was never reached
}

func killAt(t *testing.T, h, step string, env ...string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.count=1")
	cmd.Env = append(append(os.Environ(), helperVar+"="+step, homeVar+"="+h), env...)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if sc.Text() == "STOPPED" {
				stopped <- true
				return
			}
		}
		stopped <- false
	}()
	select {
	case ok := <-stopped:
		if !ok {
			cmd.Wait()
			t.Fatalf("the helper ended without reaching %s", step)
		}
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("the helper did not reach %s in time", step)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
}

func leftovers(t *testing.T, h string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(h, ".claude", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, en := range entries {
		if strings.Contains(en.Name(), ".tmp-") || strings.Contains(en.Name(), ".old-") {
			out = append(out, en.Name())
		}
	}
	return out
}

func TestAReplacementKilledHalfwayIsSettledOnTheNextRun(t *testing.T) {
	cases := []struct {
		step string
		// committed says whether the write counts as done after recovery.
		committed bool
		version   string
	}{
		{stepStaged, false, "v1"},   // new content half written next to the old directory
		{stepMovedOld, false, "v1"}, // the old directory moved aside, nothing at the target
		{stepMovedNew, true, "v2"},  // the new directory in place, the old one still aside
	}
	for _, c := range cases {
		t.Run(c.step, func(t *testing.T) {
			e := newEnv(t)
			e.apply(t, skill("v1"), Options{})
			v1 := snapshot(t, e.home)
			h := e.home
			e.close() // let the helper take the lock

			killAt(t, h, c.step)

			e = openEnv(t, h) // the next run; the dead helper's lock is gone
			r, err := e.locked.Recover(e.ex.Settle)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Unresolved) != 0 {
				t.Fatalf("Recover left %d intents unresolved", len(r.Unresolved))
			}
			if got := len(r.Committed) == 1; got != c.committed {
				t.Fatalf("Recover = %+v; want committed=%v", r, c.committed)
			}
			if l := leftovers(t, h); len(l) != 0 {
				t.Fatalf("left behind next to the target: %v", l)
			}
			if c.version == "v1" {
				assertSame(t, v1, snapshot(t, h), "after recovery the old version should be back")
			}
			op := skill(c.version)
			entries := e.entries(t)
			if len(entries) != 1 || entries[0].Digest != filesDigest(op.Files) {
				t.Fatalf("manifest after recovery: %+v, want the %s digest", entries, c.version)
			}
			if o := e.ex.Observe(TargetOf(op)); o.Digest != entries[0].Digest {
				t.Fatalf("the directory does not match the manifest: %+v", o)
			}
			e.assertNoIntents(t)
		})
	}
}

func TestAKeyEditKilledHalfwayIsSettledOnTheNextRun(t *testing.T) {
	cases := []struct {
		step      string
		committed bool
	}{
		{stepStaged, false},      // the temporary file written, the file untouched
		{stepFileChecked, false}, // about to rename
		{stepMovedNew, true},     // renamed, not yet recorded
	}
	for _, c := range cases {
		t.Run(c.step, func(t *testing.T) {
			e := keyEnv(t, claudeJSON, 0o644)
			before := snapshot(t, e.home)
			h := e.home
			e.close()

			killAt(t, h, c.step, opVar+"=key")

			e = openEnv(t, h)
			r, err := e.locked.Recover(e.ex.Settle)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Unresolved) != 0 || (len(r.Committed) == 1) != c.committed {
				t.Fatalf("Recover = %+v; want committed=%v", r, c.committed)
			}
			if l := leftoversIn(t, h); len(l) != 0 {
				t.Fatalf("left behind: %v", l)
			}
			e.assertNoIntents(t)
			entries := e.entries(t)
			if !c.committed {
				assertSame(t, before, snapshot(t, h), "after recovery the file should be as it was")
				if len(entries) != 0 {
					t.Fatalf("manifest %+v", entries)
				}
				return
			}
			if len(entries) != 1 || entries[0].Edit == nil {
				t.Fatalf("manifest %+v", entries)
			}
			// What recovery recorded undoes the edit exactly.
			if _, err := e.ex.Revert(ctx, entries[0]); err != nil {
				t.Fatal(err)
			}
			assertSame(t, before, snapshot(t, h), "undoing what recovery recorded")
		})
	}
}
