// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package check

import (
	"fmt"

	"github.com/code2rich/rigdepot/ext/internal/registry"
)

// Registry collects the rules of a rule set, keyed by their ID, together with the set's version.
//
// A registry is filled while the program starts and frozen by its first read: after All, Lookup
// or Ruleset has been called once, Register and SetVersion panic. Every check in one run of the
// program therefore uses the same rules under the same version. A Registry is safe for use by
// several goroutines.
type Registry struct {
	set     *registry.Set[Rule]
	version int
}

// NewRegistry returns an empty registry that is independent of the default one. It is meant for
// tests, which must not touch global state.
func NewRegistry() *Registry {
	return &Registry{set: registry.New("check rule", Rule.ID)}
}

// Register adds a rule. It panics when r is nil, when its ID is not lower-case letters, digits
// and single hyphens, when the ID is already registered, and when the registry has been read
// before. Each of these is a mistake in how the program is put together, and the panic makes the
// first test run show it.
func (g *Registry) Register(r Rule) {
	if r == nil {
		panic("check rule registry: Register(nil)")
	}
	g.set.Add(r)
}

// SetVersion declares the version of the rule set this registry holds. It panics when v is less
// than 1, when a version was already declared, and when the registry has been read before.
func (g *Registry) SetVersion(v int) {
	if v < 1 {
		panic(fmt.Sprintf("check rule registry: rule set version %d: versions start at 1", v))
	}
	g.set.Mutate(fmt.Sprintf("the rule set version is set to %d", v), func() {
		if g.version != 0 {
			panic(fmt.Sprintf("check rule registry: the rule set version is declared twice (%d, then %d)", g.version, v))
		}
		g.version = v
	})
}

// All returns every registered rule, sorted by ID, and freezes the registry. The slice is the
// caller's own.
func (g *Registry) All() []Rule {
	return g.set.All()
}

// Lookup returns the rule with the given ID, and freezes the registry.
func (g *Registry) Lookup(id string) (Rule, bool) {
	return g.set.Lookup(id)
}

// Ruleset returns the registered rules, sorted by ID, under the declared version, and freezes
// the registry. The version is zero when SetVersion was never called.
func (g *Registry) Ruleset() Ruleset {
	rules := g.set.All() // freezes, so the version can no longer change
	return Ruleset{Version: g.version, Rules: rules}
}

// defaultRegistry is the registry of the running program.
var defaultRegistry = NewRegistry()

// Register adds a rule to the default registry. It panics under the same conditions as
// Registry.Register.
//
// Built-in rules do not register themselves. Each exports New, and builtin/all holds one line per
// rule:
//
//	check.Register(secrets.New())
//
// so that one directory shows everything a program contains.
func Register(r Rule) {
	defaultRegistry.Register(r)
}

// SetVersion declares the version of the default rule set. builtin/all calls it once, next to
// the lines that register the rules. It panics under the same conditions as
// Registry.SetVersion.
func SetVersion(v int) {
	defaultRegistry.SetVersion(v)
}

// All returns every rule in the default registry, sorted by ID. The first call freezes the
// default registry.
func All() []Rule {
	return defaultRegistry.All()
}

// Lookup returns the rule with the given ID from the default registry, and freezes it.
func Lookup(id string) (Rule, bool) {
	return defaultRegistry.Lookup(id)
}

// DefaultRuleset returns the rule set of the running program: every rule in the default
// registry under the version builtin/all declared. It freezes the default registry.
func DefaultRuleset() Ruleset {
	return defaultRegistry.Ruleset()
}
