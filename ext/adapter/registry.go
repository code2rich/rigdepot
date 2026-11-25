// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adapter

import "github.com/code2rich/rigdepot/ext/internal/registry"

// Registry is a set of adapters keyed by their ID.
//
// A registry is filled while the program starts and frozen by its first read: after All or
// Lookup has been called once, Register panics. Everything that asks which tools are supported
// therefore gets the same answer. A Registry is safe for use by several goroutines.
type Registry struct {
	set *registry.Set[Adapter]
}

// NewRegistry returns an empty registry that is independent of the default one. It is meant for
// tests, which must not touch global state.
func NewRegistry() *Registry {
	return &Registry{set: registry.New("adapter", Adapter.ID)}
}

// Register adds an adapter. It panics when a is nil, when its ID is not lower-case letters,
// digits and single hyphens, when the ID is already registered, and when the registry has been
// read before. Each of these is a mistake in how the program is put together, and the panic
// makes the first test run show it.
func (r *Registry) Register(a Adapter) {
	if a == nil {
		panic("adapter registry: Register(nil)")
	}
	r.set.Add(a)
}

// All returns every registered adapter, sorted by ID, and freezes the registry. The slice is
// the caller's own.
func (r *Registry) All() []Adapter {
	return r.set.All()
}

// Lookup returns the adapter with the given ID, and freezes the registry.
func (r *Registry) Lookup(id string) (Adapter, bool) {
	return r.set.Lookup(id)
}

// defaultRegistry is the registry of the running program.
var defaultRegistry = NewRegistry()

// Register adds an adapter to the default registry. It panics under the same conditions as
// Registry.Register.
//
// Built-in adapters do not register themselves. Each exports New, and builtin/all holds one line
// per adapter:
//
//	adapter.Register(claudecode.New())
//
// so that one directory shows everything a program contains.
func Register(a Adapter) {
	defaultRegistry.Register(a)
}

// All returns every adapter in the default registry, sorted by ID. The first call freezes the
// default registry.
func All() []Adapter {
	return defaultRegistry.All()
}

// Lookup returns the adapter with the given ID from the default registry, and freezes it.
func Lookup(id string) (Adapter, bool) {
	return defaultRegistry.Lookup(id)
}
