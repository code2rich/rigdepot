// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package manifest

// These tests start this test binary again as separate processes, because the guarantees they
// check are about processes: two programs at once, and a program that is killed.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/home"
)

// The variables that turn the test binary into a helper. They carry the test prefix, which the
// test isolation lets through.
var (
	helperVar = brand.EnvPrefix + "TEST_MANIFEST_HELPER"
	dirVar    = brand.EnvPrefix + "TEST_MANIFEST_DIR"
	nameVar   = brand.EnvPrefix + "TEST_MANIFEST_NAME"
)

// exitLocked is the helper's exit code when it did not get the lock.
const exitLocked = 9

// helper is a running copy of this test binary.
type helper struct {
	cmd   *exec.Cmd
	lines chan string
}

func startHelper(t *testing.T, mode, dir, name string) *helper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperVar+"="+mode, dirVar+"="+dir, nameVar+"="+name)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &helper{cmd: cmd, lines: make(chan string, 16)}
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			h.lines <- sc.Text()
		}
		close(h.lines)
	}()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	return h
}

// expect waits for the helper to print want.
func (h *helper) expect(t *testing.T, want string) {
	t.Helper()
	timeout := time.After(20 * time.Second)
	for {
		select {
		case line, ok := <-h.lines:
			if !ok {
				t.Fatalf("helper ended without printing %q", want)
			}
			if line == want {
				return
			}
		case <-timeout:
			t.Fatalf("helper did not print %q in time", want)
		}
	}
}

func (h *helper) exitCode(t *testing.T) int {
	t.Helper()
	for range h.lines { // drain, so that Wait does not block on the pipe
	}
	err := h.cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0
}

func waitForFile(p string) {
	for {
		if _, err := os.Stat(p); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestHelperProcess is not a test: it is what the helpers run. Without the variable it returns
// at once.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperVar)
	if mode == "" {
		return
	}
	dir, err := home.At(os.Getenv(dirVar))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	name := os.Getenv(nameVar)
	s := Open(dir)
	ctx := context.Background()
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "helper %s: %v\n", name, err)
		os.Exit(1)
	}
	switch mode {
	case "race":
		// Wait for the starting signal, try the lock without waiting, and if it was had,
		// record an entry and hold the lock until told to let go.
		fmt.Println("READY")
		waitForFile(filepath.Join(dir.Path(), "go"))
		l, err := s.Lock(ctx, 0)
		if errors.Is(err, ErrLocked) {
			fmt.Println("LOCKED")
			os.Exit(exitLocked)
		}
		if err != nil {
			fail(err)
		}
		m, err := l.Read()
		if err != nil {
			fail(err)
		}
		m.Put(sampleEntry(name))
		if err := l.Write(m); err != nil {
			fail(err)
		}
		fmt.Println("WROTE")
		waitForFile(filepath.Join(dir.Path(), "release"))
		l.Release()
		os.Exit(0)
	case "crash":
		// Announce a file, write it, and stop dead before recording it.
		l, err := s.Lock(ctx, time.Second)
		if err != nil {
			fail(err)
		}
		target := filepath.Join(dir.Path(), "..", name)
		e := sampleEntry(name)
		e.Op, e.Path = OpFile, "~/"+name
		e.ID = EntryID(e.Tool, e.Op, e.Path, nil)
		e.Digest = digestOf([]byte("content of " + name))
		if _, err := l.Intend(Intent{Kind: Write, Entry: e, Before: "", After: e.Digest}); err != nil {
			fail(err)
		}
		if err := os.WriteFile(target, []byte("content of "+name), 0o644); err != nil {
			fail(err)
		}
		fmt.Println("WRITTEN")
		time.Sleep(time.Hour) // killed here
	}
	fail(fmt.Errorf("unknown mode %q", mode))
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestTwoProcessesWritingAtOnceOnlyOneSucceeds(t *testing.T) {
	s := newStore(t)
	if err := s.Dir().Ensure(); err != nil {
		t.Fatal(err)
	}
	a := startHelper(t, "race", s.Dir().Path(), "first")
	b := startHelper(t, "race", s.Dir().Path(), "second")
	a.expect(t, "READY")
	b.expect(t, "READY")
	if err := os.WriteFile(filepath.Join(s.Dir().Path(), "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// Whichever got the lock holds it until "release" exists, so the other one has to find it
	// taken. Wait for one of them to give up, then let the other finish.
	results := make(chan [2]any, 2)
	for _, h := range []*helper{a, b} {
		go func() {
			for line := range h.lines {
				if line == "LOCKED" || line == "WROTE" {
					results <- [2]any{h, line}
				}
			}
		}()
	}
	var winner, loser *helper
	for range 2 {
		select {
		case r := <-results:
			if r[1] == "WROTE" {
				winner = r[0].(*helper)
			} else {
				loser = r[0].(*helper)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the helpers did not report in time")
		}
	}
	if winner == nil || loser == nil {
		t.Fatalf("want one writer and one turned away, got winner=%v loser=%v", winner != nil, loser != nil)
	}
	if code := waitExit(t, loser); code != exitLocked {
		t.Fatalf("the process that did not get the lock exited with %d, want %d", code, exitLocked)
	}
	if err := os.WriteFile(filepath.Join(s.Dir().Path(), "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := waitExit(t, winner); code != 0 {
		t.Fatalf("the process with the lock exited with %d", code)
	}
	m, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 1 {
		t.Fatalf("the manifest has %d entries, want exactly the one from the process that held the lock", len(m.Entries))
	}
}

func waitExit(t *testing.T, h *helper) int {
	t.Helper()
	err := h.cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil && !strings.Contains(err.Error(), "file already closed") {
		t.Fatal(err)
	}
	return 0
}

func TestAWriteInterruptedBeforeItIsRecordedIsRecordedOnTheNextRun(t *testing.T) {
	s := newStore(t)
	if err := s.Dir().Ensure(); err != nil {
		t.Fatal(err)
	}
	h := startHelper(t, "crash", s.Dir().Path(), "skill-file")
	h.expect(t, "WRITTEN")
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	h.exitCode(t)

	if m, err := s.Read(); err != nil || len(m.Entries) != 0 {
		t.Fatalf("before recovery: %v, %v; the killed process should not have recorded anything", m, err)
	}
	// The operating system released the dead process's lock.
	l, err := s.Lock(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("Lock after the holder was killed = %v", err)
	}
	defer l.Release()
	target := filepath.Join(s.Dir().Path(), "..", "skill-file")
	r, err := l.Recover(func(in Intent) Observation {
		data, err := os.ReadFile(target)
		if errors.Is(err, os.ErrNotExist) {
			return Observation{}
		}
		if err != nil {
			return Observation{Err: err}
		}
		return Observation{Exists: true, Digest: digestOf(data)}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Committed) != 1 {
		t.Fatalf("Recover = %+v, want the interrupted write recorded", r)
	}
	m, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 1 || m.Entries[0].Path != "~/skill-file" {
		t.Fatalf("manifest after recovery: %+v", m.Entries)
	}
	if pending, _ := l.Pending(); len(pending) != 0 {
		t.Fatalf("%d intents left after recovery", len(pending))
	}
	if _, err := os.Stat(s.Dir().Join(home.JournalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the empty intent log was not removed: %v", err)
	}
}
