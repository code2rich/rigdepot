// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package kind

import "github.com/code2rich/rigdepot/ext/internal/registry"

// Registry is a set of resource kinds keyed by their ID.
//
// A registry is filled while the program starts and frozen by its first read: after All or
// Lookup has been called once, Register panics. Everything that asks which kinds exist therefore
// gets the same answer. A Registry is safe for use by several goroutines.
type Registry struct {
	set *registry.Set[Kind]
}

// NewRegistry returns an empty registry that is independent of the default one. It is meant for
// tests, which must not touch global state.
func NewRegistry() *Registry {
	return &Registry{set: registry.New("resource kind", Kind.ID)}
}

// Register adds a kind. It panics when k is nil, when its ID is not lower-case letters, digits
// and single hyphens, when the ID is already registered, and when the registry has been read
// before. Each of these is a mistake in how the program is put together, and the panic makes the
// first test run show it.
func (r *Registry) Register(k Kind) {
	if k == nil {
		panic("resource kind registry: Register(nil)")
	}
	r.set.Add(k)
}

// All returns every registered kind, sorted by ID, and freezes the registry. The slice is the
// caller's own.
func (r *Registry) All() []Kind {
	return r.set.All()
}

// Lookup returns the kind with the given ID, and freezes the registry. A resource whose kind is
// not registered is not delivered.
func (r *Registry) Lookup(id string) (Kind, bool) {
	return r.set.Lookup(id)
}

// defaultRegistry is the registry of the running program.
var defaultRegistry = NewRegistry()

// Register adds a kind to the default registry. It panics under the same conditions as
// Registry.Register.
//
// Built-in kinds do not register themselves. Each exports New, and builtin/all holds one line per
// kind:
//
//	kind.Register(skill.New())
//
// so that one directory shows everything a program contains.
func Register(k Kind) {
	defaultRegistry.Register(k)
}

// All returns every kind in the default registry, sorted by ID. The first call freezes the
// default registry.
func All() []Kind {
	return defaultRegistry.All()
}

// Lookup returns the kind with the given ID from the default registry, and freezes it.
func Lookup(id string) (Kind, bool) {
	return defaultRegistry.Lookup(id)
}
