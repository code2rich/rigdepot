// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package module

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

type fake struct{ id string }

func (f fake) ID() string                      { return f.id }
func (fake) Describe() Info                    { return Info{} }
func (fake) Start(context.Context, Host) error { return nil }

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

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	r.Register(fake{"review"})
	r.Register(fake{"insights"})
	var ids []string
	for _, m := range r.All() {
		ids = append(ids, m.ID())
	}
	if !slices.Equal(ids, []string{"insights", "review"}) {
		t.Errorf("All() = %v", ids)
	}
	if _, ok := r.Lookup("review"); !ok {
		t.Error("Lookup did not find a registered module")
	}
	if msg := mustPanic(t, "registering after the first read", func() { r.Register(fake{"env"}) }); !strings.Contains(msg, `"env"`) {
		t.Errorf("panic message %q does not name the module", msg)
	}
}

func TestRegistryRejects(t *testing.T) {
	for name, m := range map[string]Module{
		"nil":       nil,
		"malformed": fake{"AI_review"},
		"reserved":  fake{"core"},
	} {
		mustPanic(t, "registering a "+name+" module", func() { NewRegistry().Register(m) })
	}
	r := NewRegistry()
	r.Register(fake{"insights"})
	mustPanic(t, "registering the same ID twice", func() { r.Register(fake{"insights"}) })
}
