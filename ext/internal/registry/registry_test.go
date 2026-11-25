// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package registry

import (
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func newSet() *Set[string] {
	return New("widget", func(s string) string { return s })
}

// panicMessage runs fn and returns what it panicked with, or "" when it returned normally.
func panicMessage(t *testing.T, fn func()) (msg string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			s, ok := r.(string)
			if !ok {
				t.Fatalf("panic value is %T, want a string a person can read: %v", r, r)
			}
			msg = s
		}
	}()
	fn()
	return ""
}

func TestAllReturnsItemsSortedByIdentifier(t *testing.T) {
	s := newSet()
	// Enough items that an unsorted result cannot pass by chance.
	for _, id := range []string{"opencode", "zcode", "claude-code", "kimi", "codex", "a10", "gemini", "a9", "b", "cursor", "aider", "a"} {
		s.Add(id)
	}
	got := s.All()
	want := []string{"a", "a10", "a9", "aider", "b", "claude-code", "codex", "cursor", "gemini", "kimi", "opencode", "zcode"}
	if !slices.Equal(got, want) {
		t.Errorf("All() = %v, want %v", got, want)
	}
}

func TestAllReturnsACopy(t *testing.T) {
	s := newSet()
	s.Add("codex")
	s.Add("opencode")
	first := s.All()
	first[0] = "tampered"
	if got := s.All(); !slices.Equal(got, []string{"codex", "opencode"}) {
		t.Errorf("a caller changed its slice and the registry changed with it: All() = %v", got)
	}
}

func TestAddPanicsOnDuplicateIdentifier(t *testing.T) {
	s := newSet()
	s.Add("codex")
	msg := panicMessage(t, func() { s.Add("codex") })
	if msg == "" {
		t.Fatal("registering the same identifier twice did not panic")
	}
	for _, part := range []string{"widget", `"codex"`, "twice"} {
		if !strings.Contains(msg, part) {
			t.Errorf("panic message %q does not mention %s", msg, part)
		}
	}
}

func TestAddPanicsAfterFirstRead(t *testing.T) {
	reads := map[string]func(*Set[string]){
		"All":    func(s *Set[string]) { s.All() },
		"Lookup": func(s *Set[string]) { s.Lookup("codex") },
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			s := newSet()
			s.Add("codex")
			read(s)
			msg := panicMessage(t, func() { s.Add("opencode") })
			if msg == "" {
				t.Fatalf("registering after %s did not panic", name)
			}
			if !strings.Contains(msg, `"opencode"`) || !strings.Contains(msg, "first read") {
				t.Errorf("panic message %q does not say which item came too late", msg)
			}
			if got := s.All(); !slices.Equal(got, []string{"codex"}) {
				t.Errorf("the late item got in anyway: All() = %v", got)
			}
		})
	}
}

func TestAddPanicsOnMalformedIdentifier(t *testing.T) {
	for _, id := range []string{"", "Claude-Code", "claude_code", "claude code", "-codex", "codex-", "co--dex", "codex/2", "工具"} {
		s := newSet()
		if msg := panicMessage(t, func() { s.Add(id) }); !strings.Contains(msg, "malformed") {
			t.Errorf("Add(%q): panic message %q, want one that says the identifier is malformed", id, msg)
		}
		if got := s.All(); len(got) != 0 {
			t.Errorf("Add(%q) left %v in the registry", id, got)
		}
	}
}

func TestValidID(t *testing.T) {
	for _, id := range []string{"a", "codex", "claude-code", "a1-b2-c3", "9lives"} {
		if !ValidID(id) {
			t.Errorf("ValidID(%q) = false, want true", id)
		}
	}
}

func TestLookup(t *testing.T) {
	s := newSet()
	s.Add("codex")
	if got, ok := s.Lookup("codex"); !ok || got != "codex" {
		t.Errorf(`Lookup("codex") = %q, %v`, got, ok)
	}
	if _, ok := s.Lookup("claude-code"); ok {
		t.Error(`Lookup("claude-code") found an item that was never registered`)
	}
}

func TestMutateRunsUntilFirstRead(t *testing.T) {
	s := newSet()
	ran := false
	s.Mutate("setting the colour", func() { ran = true })
	if !ran {
		t.Fatal("Mutate did not run the function before the first read")
	}
	s.All()
	ran = false
	msg := panicMessage(t, func() { s.Mutate("setting the colour", func() { ran = true }) })
	if ran {
		t.Error("Mutate ran the function after the first read")
	}
	if !strings.Contains(msg, "setting the colour") {
		t.Errorf("panic message %q does not say what was attempted", msg)
	}
}

// The registries are filled from init functions and read from wherever the program needs them.
// Run with -race, this fails if a read and a late registration can touch the map together.
func TestConcurrentReads(t *testing.T) {
	s := newSet()
	s.Add("codex")
	s.Add("opencode")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := s.All(); len(got) != 2 {
				t.Errorf("All() returned %d items, want 2", len(got))
			}
			if _, ok := s.Lookup("codex"); !ok {
				t.Error(`Lookup("codex") found nothing`)
			}
		}()
	}
	wg.Wait()
}
