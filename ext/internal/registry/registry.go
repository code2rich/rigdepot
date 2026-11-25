// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package registry

import (
	"fmt"
	"regexp"
	"slices"
	"sync"
)

// idPattern is the shape every identifier has: lower-case letters and digits in groups joined by
// single hyphens, such as "claude-code".
var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidID reports whether id has the shape every registry requires.
func ValidID(id string) bool {
	return idPattern.MatchString(id)
}

// Set holds the items of one extension point. The zero value is not usable; create one with New.
//
// A Set has two phases. While the program starts, items are added with Add. The first read (All
// or Lookup) freezes the set, and from then on Add panics. That way everything that reads the set
// sees the same items, however early it runs.
type Set[T any] struct {
	what string // what an item is called in panic messages, such as "adapter"
	id   func(T) string

	mu     sync.Mutex
	items  map[string]T
	frozen bool
}

// New returns an empty set. what names one item in panic messages ("adapter", "resource kind");
// id returns the identifier of an item.
func New[T any](what string, id func(T) string) *Set[T] {
	return &Set[T]{what: what, id: id, items: map[string]T{}}
}

// Add registers an item. It panics when the identifier is malformed or already taken, and when
// the set has been read before. Each of these is a mistake in how the program is put together,
// so it must stop the program at start-up instead of surfacing later as a missing item.
func (s *Set[T]) Add(item T) {
	id := s.id(item)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !ValidID(id):
		panic(fmt.Sprintf("%s registry: identifier %q is malformed: use lower-case letters, digits and single hyphens", s.what, id))
	case s.frozen:
		panic(fmt.Sprintf("%s registry: %q is registered after the registry was first read: register everything before the program starts working", s.what, id))
	}
	if _, taken := s.items[id]; taken {
		panic(fmt.Sprintf("%s registry: %q is registered twice", s.what, id))
	}
	s.items[id] = item
}

// Mutate runs fn unless the set has been read before, in which case it panics with a message
// that names what was attempted. Registries use it for settings that travel with the set and
// must be frozen together with it.
func (s *Set[T]) Mutate(attempt string, fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen {
		panic(fmt.Sprintf("%s registry: %s after the registry was first read", s.what, attempt))
	}
	fn()
}

// All freezes the set and returns its items sorted by identifier. The slice is the caller's own.
func (s *Set[T]) All() []T {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frozen = true
	ids := make([]string, 0, len(s.items))
	for id := range s.items {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]T, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.items[id])
	}
	return out
}

// Lookup freezes the set and returns the item with the given identifier.
func (s *Set[T]) Lookup(id string) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frozen = true
	item, ok := s.items[id]
	return item, ok
}
