// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package identity

import "github.com/code2rich/rigdepot/ext/internal/registry"

// Registry is a set of identity source kinds keyed by their ID.
//
// A registry is filled while the program starts and frozen by its first read: after All or
// Lookup has been called once, Register panics. A Registry is safe for use by several
// goroutines.
type Registry struct {
	set *registry.Set[Provider]
}

// NewRegistry returns an empty registry that is independent of the default one. It is meant for
// tests, which must not touch global state.
func NewRegistry() *Registry {
	return &Registry{set: registry.New("identity source", Provider.ID)}
}

// Register adds a source. It panics when p is nil, when its ID is malformed or already
// registered, and when the registry has been read before. Each of these is a mistake in how the
// program is put together, and the panic makes the first test run show it.
func (r *Registry) Register(p Provider) {
	if p == nil {
		panic("identity source registry: Register(nil)")
	}
	r.set.Add(p)
}

// All returns every registered source, sorted by ID, and freezes the registry. The slice is
// the caller's own.
func (r *Registry) All() []Provider {
	return r.set.All()
}

// Lookup returns the source with the given ID, and freezes the registry.
func (r *Registry) Lookup(id string) (Provider, bool) {
	return r.set.Lookup(id)
}

// defaultRegistry is the registry of the running program.
var defaultRegistry = NewRegistry()

// Register adds a source to the default registry. It panics under the same conditions as
// Registry.Register.
//
// Built-in sources do not register themselves. Each exports New, and builtin/all/server holds
// one line per source:
//
//	identity.Register(oidc.New())
func Register(p Provider) {
	defaultRegistry.Register(p)
}

// All returns every source in the default registry, sorted by ID. The first call freezes the
// default registry.
func All() []Provider {
	return defaultRegistry.All()
}

// Lookup returns the source with the given ID from the default registry, and freezes it.
func Lookup(id string) (Provider, bool) {
	return defaultRegistry.Lookup(id)
}
