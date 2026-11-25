// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/code2rich/rigdepot/schema"
)

// ErrUnknown is what every error that means "the local state cannot be known" satisfies, with
// errors.Is. The command line maps it to exit code 9 and points at the doctor command.
var ErrUnknown = errors.New("the local state is unknown")

// Reason says why the state is unknown. The values are stable and are used in machine-readable
// output.
type Reason string

const (
	// Unreadable: the file exists but could not be opened or read (permissions, a directory
	// where a file should be, an I/O error).
	Unreadable Reason = "unreadable"
	// Malformed: the file is not JSON, is empty, or is cut off.
	Malformed Reason = "malformed"
	// Newer: the file was written by a newer program in a format this one does not know.
	Newer Reason = "newer_format"
	// Invalid: the file is JSON but does not match the schema, or breaks a rule the schema
	// cannot express (two entries with the same ID).
	Invalid Reason = "invalid"
	// Locked: another program holds the lock.
	LockHeld Reason = "locked"
)

// UnknownError is the error returned when the manifest or the intent log exists and cannot be
// trusted. It satisfies errors.Is(err, ErrUnknown).
type UnknownError struct {
	Path   string
	Reason Reason
	Err    error
}

func (e *UnknownError) Error() string {
	msg := fmt.Sprintf("%s: %s", e.Path, e.Reason)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg + " (the local state is unknown; nothing was changed)"
}

func (e *UnknownError) Unwrap() error { return e.Err }

// Is reports whether target is ErrUnknown, or ErrLocked for a lock that could not be had.
func (e *UnknownError) Is(target error) bool {
	return target == ErrUnknown || (target == ErrLocked && e.Reason == LockHeld)
}

func unknown(path string, r Reason, err error) error {
	return &UnknownError{Path: path, Reason: r, Err: err}
}

// ReadFile reads the manifest at path.
//
// A file that does not exist gives an empty manifest and no error: nothing was ever written on
// this computer. Every other failure gives an *UnknownError and no manifest.
func ReadFile(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Manifest{Schema: Version, Entries: []Entry{}}, nil
	}
	if err != nil {
		return nil, unknown(path, Unreadable, err)
	}
	return Parse(path, data)
}

// Parse decodes and checks the content of a manifest. path is only used in errors.
func Parse(path string, data []byte) (*Manifest, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, unknown(path, Malformed, errors.New("the file is empty"))
	}
	var head struct {
		Schema json.RawMessage `json:"schema"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, unknown(path, Malformed, err)
	}
	var version int
	if err := json.Unmarshal(head.Schema, &version); err == nil && version > Version {
		return nil, unknown(path, Newer, fmt.Errorf("format version %d; this program reads up to %d", version, Version))
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, unknown(path, Malformed, err)
	}
	sch, err := compiled()
	if err != nil {
		return nil, err
	}
	if err := sch.Validate(doc); err != nil {
		return nil, unknown(path, Invalid, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, unknown(path, Invalid, err)
	}
	if err := upgrade(&m); err != nil {
		return nil, unknown(path, Invalid, err)
	}
	seen := map[string]bool{}
	for _, e := range m.Entries {
		if seen[e.ID] {
			return nil, unknown(path, Invalid, fmt.Errorf("two entries have the id %q", e.ID))
		}
		seen[e.ID] = true
	}
	if m.Entries == nil {
		m.Entries = []Entry{}
	}
	return &m, nil
}

// upgrade brings a manifest of an older format to Version, in memory. It is written back in
// the new format the next time the manifest is written. Version 1 is the first format, so there
// is nothing to upgrade yet; every new version adds a step here, a fixture of the old format,
// and a test that the fixture still reads.
func upgrade(m *Manifest) error {
	if m.Schema != Version {
		return fmt.Errorf("format version %d is not supported", m.Schema)
	}
	return nil
}

// check validates a manifest about to be written against the schema. A failure is a defect in
// this program: the manifest it writes has to be one it can read back.
func check(m *Manifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	sch, err := compiled()
	if err != nil {
		return err
	}
	if err := sch.Validate(doc); err != nil {
		return fmt.Errorf("manifest: refusing to write a manifest that does not match its schema (a defect): %w", err)
	}
	return nil
}

var (
	compileOnce sync.Once
	compiledSch *jsonschema.Schema
	compileErr  error
)

// compiled returns the manifest schema, compiled once. Nothing is fetched over the network:
// the schema is embedded and has no references outside itself.
func compiled() (*jsonschema.Schema, error) {
	compileOnce.Do(func() {
		data, err := fs.ReadFile(schema.FS(), schema.Manifest)
		if err != nil {
			compileErr = err
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			compileErr = err
			return
		}
		obj, _ := doc.(map[string]any)
		id, _ := obj["$id"].(string)
		c := jsonschema.NewCompiler()
		if compileErr = c.AddResource(id, doc); compileErr != nil {
			return
		}
		compiledSch, compileErr = c.Compile(id)
	})
	if compileErr != nil {
		return nil, fmt.Errorf("manifest: the embedded schema does not compile (a defect): %w", compileErr)
	}
	return compiledSch, nil
}
