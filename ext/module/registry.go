// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package module

import "github.com/code2rich/rigdepot/ext/internal/registry"

// Reserved are identifiers no module may take: "core" is the migration namespace of the core
// tables.
var Reserved = []string{"core"}

// Registry is a set of modules keyed by their ID.
//
// A registry is filled while the program starts and frozen by its first read: after All or
// Lookup has been called once, Register panics. A Registry is safe for use by several
// goroutines.
type Registry struct {
	set *registry.Set[Module]
}

// NewRegistry returns an empty registry that is independent of the default one. It is meant for
// tests, which must not touch global state.
func NewRegistry() *Registry {
	return &Registry{set: registry.New("module", Module.ID)}
}

// Register adds a module. It panics when m is nil, when its ID is malformed, reserved or already
// registered, and when the registry has been read before. Each of these is a mistake in how the
// program is put together, and the panic makes the first test run show it.
func (r *Registry) Register(m Module) {
	if m == nil {
		panic("module registry: Register(nil)")
	}
	for _, id := range Reserved {
		if m.ID() == id {
			panic("module registry: the identifier \"" + id + "\" is reserved")
		}
	}
	r.set.Add(m)
}

// All returns every registered module, sorted by ID, and freezes the registry. The slice is the
// caller's own.
func (r *Registry) All() []Module {
	return r.set.All()
}

// Lookup returns the module with the given ID, and freezes the registry.
func (r *Registry) Lookup(id string) (Module, bool) {
	return r.set.Lookup(id)
}

// defaultRegistry is the registry of the running program.
var defaultRegistry = NewRegistry()

// Register adds a module to the default registry. It panics under the same conditions as
// Registry.Register.
//
// Modules do not register themselves. Each exports New, and modules/all holds one file per
// module, maybe_<id>.go, with the build constraint !rgd_omit_<id> && !rgd_core_only:
//
//	func init() { module.Register(insights.New()) }
func Register(m Module) {
	defaultRegistry.Register(m)
}

// All returns every module in the default registry, sorted by ID. The first call freezes the
// default registry.
func All() []Module {
	return defaultRegistry.All()
}

// Lookup returns the module with the given ID from the default registry, and freezes it.
func Lookup(id string) (Module, bool) {
	return defaultRegistry.Lookup(id)
}
