// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package keyring

import (
	"fmt"
	"sync"
)

// Memory is a Store that keeps its entries in memory, for the tests of packages that use a Store.
// It accepts the same names and values as the real one. Fail makes it behave like a keychain that
// cannot be read. Its methods are safe for concurrent use.
type Memory struct {
	mu      sync.Mutex
	entries map[string][]byte
	kind    Kind
	err     error
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty store of kind System.
func NewMemory() *Memory {
	return &Memory{entries: map[string][]byte{}, kind: System}
}

// SetKind changes what Kind reports, such as PlainFile for a test of the fallback notice.
func (m *Memory) SetKind(k Kind) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kind = k
}

// Fail makes every following call return err (and Kind return Unknown), as a locked or
// unreachable keychain would; Fail(nil) makes the store work again. The entries stay.
func (m *Memory) Fail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// Get returns a copy of the value; see Store.
func (m *Memory) Get(name string) ([]byte, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	v, ok := m.entries[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return append([]byte(nil), v...), nil
}

// Set stores a copy of the value; see Store.
func (m *Memory) Set(name string, value []byte) error {
	if err := checkName(name); err != nil {
		return err
	}
	if err := checkValue(value); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.entries[name] = append([]byte(nil), value...)
	return nil
}

// Delete removes the entry; see Store.
func (m *Memory) Delete(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	delete(m.entries, name)
	return nil
}

// Kind reports the kind set with SetKind; Unknown while the store fails.
func (m *Memory) Kind() Kind {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return Unknown
	}
	return m.kind
}
