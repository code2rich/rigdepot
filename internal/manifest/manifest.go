// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package manifest

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Version is the manifest format this program reads and writes.
const Version = 1

// Op says what an entry records.
type Op string

const (
	OpTree Op = "tree" // a directory of ours
	OpFile Op = "file" // a file of ours
	OpKey  Op = "key"  // one key in a tool's configuration file
	OpExec Op = "exec" // something installed with the tool's own command
)

// Manifest is the content of the manifest file.
type Manifest struct {
	// Schema is the format version. Read upgrades older versions in memory, so it is always
	// Version after a successful Read.
	Schema int `json:"schema"`
	// RGD is the version of the program that last wrote the manifest.
	RGD    string  `json:"rgd,omitempty"`
	Source *Source `json:"source,omitempty"`
	// Entries are in the order they were written.
	Entries []Entry `json:"entries"`

	extra map[string]json.RawMessage
}

// Source is where the resources came from at the last sync.
type Source struct {
	Type string `json:"type"` // "server" or "repo"
	URL  string `json:"url,omitempty"`

	extra map[string]json.RawMessage
}

// Entry is one place written. The fields are described in schema/manifest/v1.
type Entry struct {
	// ID identifies the target, such as "claude-code:tree:~/.claude/skills/<name>". It is
	// unique in the manifest; see EntryID.
	ID       string `json:"id"`
	Tool     string `json:"tool"`
	Resource string `json:"resource"`
	Version  int    `json:"version,omitempty"`
	Op       Op     `json:"op"`
	// Path is "~/…" inside the home directory and absolute otherwise, with forward slashes.
	Path       string    `json:"path,omitempty"`
	Format     string    `json:"format,omitempty"`
	Key        []string  `json:"key,omitempty"`
	Digest     string    `json:"digest,omitempty"`
	Backup     string    `json:"backup,omitempty"`
	Undo       []string  `json:"undo,omitempty"`
	SharedWith []string  `json:"sharedWith,omitempty"`
	At         time.Time `json:"at"`

	extra map[string]json.RawMessage
}

// EntryID returns the identifier of the entry for a target: tool, operation and place, and for
// a key the key path after a "#", joined with dots.
func EntryID(tool string, op Op, path string, key []string) string {
	id := tool + ":" + string(op) + ":" + path
	if len(key) > 0 {
		id += "#"
		for i, k := range key {
			if i > 0 {
				id += "."
			}
			id += k
		}
	}
	return id
}

// Find returns the entry with the given ID.
func (m *Manifest) Find(id string) (Entry, bool) {
	for _, e := range m.Entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Put records e, replacing the entry with the same ID in place or appending it.
func (m *Manifest) Put(e Entry) {
	for i := range m.Entries {
		if m.Entries[i].ID == e.ID {
			m.Entries[i] = e
			return
		}
	}
	m.Entries = append(m.Entries, e)
}

// Delete removes the entry with the given ID and reports whether there was one.
func (m *Manifest) Delete(id string) bool {
	for i := range m.Entries {
		if m.Entries[i].ID == id {
			m.Entries = append(m.Entries[:i], m.Entries[i+1:]...)
			return true
		}
	}
	return false
}

// The JSON methods keep unknown fields. Each type is encoded through an alias without methods,
// and the unknown fields are appended after the known ones, sorted, so that the output is
// stable.

type manifestJSON Manifest

func (m Manifest) MarshalJSON() ([]byte, error) {
	if m.Entries == nil {
		m.Entries = []Entry{}
	}
	return withExtra(manifestJSON(m), m.extra)
}

func (m *Manifest) UnmarshalJSON(data []byte) error {
	var a manifestJSON
	extra, err := splitExtra(data, &a)
	if err != nil {
		return err
	}
	*m = Manifest(a)
	m.extra = extra
	return nil
}

type sourceJSON Source

func (s Source) MarshalJSON() ([]byte, error) { return withExtra(sourceJSON(s), s.extra) }

func (s *Source) UnmarshalJSON(data []byte) error {
	var a sourceJSON
	extra, err := splitExtra(data, &a)
	if err != nil {
		return err
	}
	*s = Source(a)
	s.extra = extra
	return nil
}

type entryJSON Entry

func (e Entry) MarshalJSON() ([]byte, error) { return withExtra(entryJSON(e), e.extra) }

func (e *Entry) UnmarshalJSON(data []byte) error {
	var a entryJSON
	extra, err := splitExtra(data, &a)
	if err != nil {
		return err
	}
	*e = Entry(a)
	e.extra = extra
	return nil
}

// splitExtra decodes data into known and returns the fields known has no place for.
func splitExtra(data []byte, known any) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(data, known); err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	names := fieldNames(known)
	extra := map[string]json.RawMessage{}
	for k, v := range all {
		if !names[k] {
			extra[k] = v
		}
	}
	if len(extra) == 0 {
		return nil, nil
	}
	return extra, nil
}

// withExtra encodes known and appends the extra fields.
func withExtra(known any, extra map[string]json.RawMessage) ([]byte, error) {
	data, err := json.Marshal(known)
	if err != nil || len(extra) == 0 {
		return data, err
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.Write(bytes.TrimSuffix(data, []byte("}")))
	for i, k := range keys {
		if i > 0 || len(data) > 2 {
			b.WriteByte(',')
		}
		name, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(extra[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// fieldNames returns the JSON names of the fields of the struct v points to, from their tags.
func fieldNames(v any) map[string]bool {
	t := reflect.TypeOf(v).Elem()
	names := map[string]bool{}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			name = f.Name
		}
		names[name] = true
	}
	return names
}
