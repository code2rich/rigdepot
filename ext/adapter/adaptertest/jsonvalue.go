// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adaptertest

// How the suite reads JSON and JSONC values: to describe a plan, and to compare what a sync did
// to a configuration file with what it was asked to do.

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/code2rich/rigdepot/ext/adapter"
)

// errUnparsable: the file does not parse, so nothing may be written to it.
var errUnparsable = errors.New("the file does not parse")

// canonical returns a JSON value in one spelling: maps sorted, no spaces, numbers as JSON has
// them. The value an operation sets and the value read back from a file compare equal this way.
func canonical(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var back any
	if err := json.Unmarshal(b, &back); err != nil {
		return "", err
	}
	b, err = json.Marshal(back)
	return string(b), err
}

// decodeJSON parses a JSON or JSONC file into generic values.
func decodeJSON(data []byte, format adapter.Format) (any, error) {
	var doc any
	dec := json.NewDecoder(bytes.NewReader(adapter.StandardJSON(data, format == adapter.JSONC)))
	if err := dec.Decode(&doc); err != nil {
		return nil, errUnparsable
	}
	if _, err := dec.Token(); err == nil {
		return nil, errUnparsable
	}
	return doc, nil
}

// lookup returns the value at path, and whether it exists.
func lookup(doc any, path []string) (any, bool) {
	cur := doc
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// without returns a copy of doc with the key at path removed.
func without(doc any, path []string) any {
	m, ok := doc.(map[string]any)
	if !ok || len(path) == 0 {
		return doc
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	if len(path) == 1 {
		delete(out, path[0])
	} else if child, ok := m[path[0]]; ok {
		out[path[0]] = without(child, path[1:])
	}
	return out
}
