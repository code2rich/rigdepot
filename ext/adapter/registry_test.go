// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adapter

import (
	"slices"
	"strings"
	"testing"
)

func ids(adapters []Adapter) []string {
	out := make([]string, 0, len(adapters))
	for _, a := range adapters {
		out = append(out, a.ID())
	}
	return out
}

// mustPanic runs fn and returns what it panicked with. The test fails if fn returns normally.
func mustPanic(t *testing.T, what string, fn func()) (msg string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("%s did not panic", what)
		}
		msg, _ = r.(string)
	}()
	fn()
	return ""
}

func TestRegistryReturnsAdaptersSortedByID(t *testing.T) {
	r := NewRegistry()
	r.Register(fake{id: "opencode"})
	r.Register(fake{id: "claude-code"})
	r.Register(fake{id: "codex"})
	if got, want := ids(r.All()), []string{"claude-code", "codex", "opencode"}; !slices.Equal(got, want) {
		t.Errorf("All() = %v, want %v", got, want)
	}
}

func TestRegistryPanicsOnDuplicateID(t *testing.T) {
	r := NewRegistry()
	r.Register(fake{id: "codex"})
	msg := mustPanic(t, "registering the ID codex twice", func() { r.Register(fake{id: "codex"}) })
	if !strings.Contains(msg, "adapter") || !strings.Contains(msg, `"codex"`) {
		t.Errorf("panic message %q does not say which adapter is registered twice", msg)
	}
}

func TestRegistryFreezesOnFirstRead(t *testing.T) {
	t.Run("All", func(t *testing.T) {
		r := NewRegistry()
		r.Register(fake{id: "codex"})
		r.All()
		mustPanic(t, "registering after All", func() { r.Register(fake{id: "opencode"}) })
		if got := ids(r.All()); !slices.Equal(got, []string{"codex"}) {
			t.Errorf("the late adapter got in anyway: All() = %v", got)
		}
	})
	t.Run("Lookup", func(t *testing.T) {
		r := NewRegistry()
		r.Register(fake{id: "codex"})
		if a, ok := r.Lookup("codex"); !ok || a.ID() != "codex" {
			t.Fatalf(`Lookup("codex") = %v, %v`, a, ok)
		}
		if _, ok := r.Lookup("opencode"); ok {
			t.Fatal(`Lookup("opencode") found an adapter that was never registered`)
		}
		mustPanic(t, "registering after Lookup", func() { r.Register(fake{id: "opencode"}) })
	})
}

func TestRegistryPanicsOnNilAndMalformedID(t *testing.T) {
	r := NewRegistry()
	mustPanic(t, "Register(nil)", func() { r.Register(nil) })
	for _, id := range []string{"", "Claude Code", "claude_code"} {
		msg := mustPanic(t, "registering the ID "+id, func() { r.Register(fake{id: id}) })
		if !strings.Contains(msg, "malformed") {
			t.Errorf("Register with ID %q: panic message %q", id, msg)
		}
	}
	if got := r.All(); len(got) != 0 {
		t.Errorf("a refused adapter is in the registry: %v", ids(got))
	}
}

func TestRegistriesAreIndependent(t *testing.T) {
	a, b := NewRegistry(), NewRegistry()
	a.Register(fake{id: "codex"})
	a.All()
	b.Register(fake{id: "codex"}) // neither the item nor the frozen state leaks from a
	if got := ids(b.All()); !slices.Equal(got, []string{"codex"}) {
		t.Errorf("b.All() = %v", got)
	}
}

// The package-level functions are the registry of the running program. This is the only test
// that touches it; nothing registers adapters in this test binary.
func TestDefaultRegistry(t *testing.T) {
	Register(fake{id: "opencode"})
	Register(fake{id: "codex"})
	if got, want := ids(All()), []string{"codex", "opencode"}; !slices.Equal(got, want) {
		t.Errorf("All() = %v, want %v", got, want)
	}
	if a, ok := Lookup("codex"); !ok || a.ID() != "codex" {
		t.Errorf(`Lookup("codex") = %v, %v`, a, ok)
	}
	mustPanic(t, "Register after the default registry was read", func() { Register(fake{id: "claude-code"}) })
}
