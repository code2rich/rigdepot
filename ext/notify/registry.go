// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package notify

import "github.com/code2rich/rigdepot/ext/internal/registry"

// Registry is a set of channels keyed by their ID.
//
// A registry is filled while the program starts and frozen by its first read: after All or
// Lookup has been called once, Register panics. A Registry is safe for use by several
// goroutines.
type Registry struct {
	set *registry.Set[Channel]
}

// NewRegistry returns an empty registry that is independent of the default one. It is meant for
// tests, which must not touch global state.
func NewRegistry() *Registry {
	return &Registry{set: registry.New("notification channel", Channel.ID)}
}

// Register adds a channel. It panics when c is nil, when its ID is malformed or already
// registered, and when the registry has been read before. Each of these is a mistake in how the
// program is put together, and the panic makes the first test run show it.
func (r *Registry) Register(c Channel) {
	if c == nil {
		panic("notification channel registry: Register(nil)")
	}
	r.set.Add(c)
}

// All returns every registered channel, sorted by ID, and freezes the registry. The slice is
// the caller's own.
func (r *Registry) All() []Channel {
	return r.set.All()
}

// Lookup returns the channel with the given ID, and freezes the registry.
func (r *Registry) Lookup(id string) (Channel, bool) {
	return r.set.Lookup(id)
}

// defaultRegistry is the registry of the running program.
var defaultRegistry = NewRegistry()

// Register adds a channel to the default registry. It panics under the same conditions as
// Registry.Register.
//
// Built-in channels do not register themselves. Each exports New, and builtin/all/server holds
// one line per channel:
//
//	notify.Register(email.New())
func Register(c Channel) {
	defaultRegistry.Register(c)
}

// All returns every channel in the default registry, sorted by ID. The first call freezes the
// default registry.
func All() []Channel {
	return defaultRegistry.All()
}

// Lookup returns the channel with the given ID from the default registry, and freezes it.
func Lookup(id string) (Channel, bool) {
	return defaultRegistry.Lookup(id)
}
