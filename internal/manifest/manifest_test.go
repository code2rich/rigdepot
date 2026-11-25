// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/testenv"
	"github.com/code2rich/rigdepot/schema"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// fixtures are the schema's own samples of valid manifests.
var fixtures = filepath.Join("..", "..", "schema", "manifest", "v1", "testdata", "manifest", "valid")

func newStore(t *testing.T) *Store {
	t.Helper()
	d, err := home.At(filepath.Join(t.TempDir(), brand.Home))
	if err != nil {
		t.Fatal(err)
	}
	return Open(d)
}

func writeRaw(t *testing.T, s *Store, data string) {
	t.Helper()
	if err := s.Dir().Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Dir().Join(home.ManifestFile), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// sampleEntry returns a valid tree entry for the named resource.
func sampleEntry(code string) Entry {
	p := "~/.claude/skills/" + brand.Prefix + code
	return Entry{
		ID:       EntryID("claude-code", OpTree, p, nil),
		Tool:     "claude-code",
		Resource: code,
		Op:       OpTree,
		Path:     p,
		Digest:   "sha256:" + strings.Repeat("ab", 32),
		At:       time.Date(2026, 11, 24, 10, 2, 11, 0, time.UTC),
	}
}

func lock(t *testing.T, s *Store) *Locked {
	t.Helper()
	l, err := s.Lock(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Release() })
	return l
}

func TestAMissingManifestIsEmpty(t *testing.T) {
	m, err := newStore(t).Read()
	if err != nil {
		t.Fatalf("Read = %v; a computer where nothing was written yet has an empty manifest", err)
	}
	if m.Schema != Version || m.Entries == nil || len(m.Entries) != 0 {
		t.Fatalf("Read = %+v, want an empty manifest of version %d", m, Version)
	}
}

// assertUnknown checks the promise of this package: a manifest that cannot be trusted is never
// returned as a manifest, empty or not.
func assertUnknown(t *testing.T, m *Manifest, err error, want Reason) {
	t.Helper()
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("err = %v, want one that satisfies ErrUnknown", err)
	}
	if m != nil {
		t.Fatalf("returned a manifest (%d entries) along with the error; an unknown manifest is never empty", len(m.Entries))
	}
	var ue *UnknownError
	if !errors.As(err, &ue) || ue.Reason != want {
		t.Fatalf("err = %v, want reason %q", err, want)
	}
}

func TestACorruptManifestIsUnknownNotEmpty(t *testing.T) {
	cases := map[string]string{
		"empty file":    "",
		"only spaces":   " \n",
		"not json":      "manifest, version one",
		"cut off":       `{"schema": 1, "entries": [{"id": "claude-code:tree:~/.claude/skills/x"`,
		"json, no root": `[]`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			writeRaw(t, s, data)
			m, err := s.Read()
			assertUnknown(t, m, err, Malformed)
		})
	}
}

func TestAManifestThatBreaksTheSchemaIsUnknown(t *testing.T) {
	e, _ := json.Marshal(sampleEntry("unit-testing"))
	cases := map[string]string{
		"version quoted":     `{"schema": "1", "entries": []}`,
		"entries missing":    `{"schema": 1}`,
		"digest missing":     `{"schema": 1, "entries": [{"id": "a", "tool": "codex", "resource": "jira", "op": "file", "path": "~/x", "at": "2026-11-24T10:02:11Z"}]}`,
		"duplicate entry id": `{"schema": 1, "entries": [` + string(e) + `,` + string(e) + `]}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			writeRaw(t, s, data)
			m, err := s.Read()
			assertUnknown(t, m, err, Invalid)
		})
	}
}

func TestAManifestOfANewerFormatIsUnknown(t *testing.T) {
	s := newStore(t)
	writeRaw(t, s, `{"schema": 2, "entries": [], "somethingNew": true}`)
	m, err := s.Read()
	assertUnknown(t, m, err, Newer)
}

func TestAnUnreadableManifestIsUnknown(t *testing.T) {
	t.Run("a directory where the file should be", func(t *testing.T) {
		s := newStore(t)
		if err := os.MkdirAll(s.Dir().Join(home.ManifestFile), 0o700); err != nil {
			t.Fatal(err)
		}
		m, err := s.Read()
		assertUnknown(t, m, err, Unreadable)
	})
	t.Run("no permission", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("permission bits do not stop the owner on Windows; the directory case above covers unreadable here")
		}
		if os.Geteuid() == 0 {
			t.Skip("root reads files regardless of permission bits; the directory case above covers unreadable here")
		}
		s := newStore(t)
		writeRaw(t, s, `{"schema": 1, "entries": []}`)
		p := s.Dir().Join(home.ManifestFile)
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(p, 0o600) })
		m, err := s.Read()
		assertUnknown(t, m, err, Unreadable)
	})
}

func TestEveryValidFixtureReadsAndKeepsUnknownFields(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtures, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in %s (%v)", fixtures, err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			m, err := Parse(f, data)
			if err != nil {
				t.Fatalf("a manifest the schema accepts does not read: %v", err)
			}
			// Written and read again, the manifest says the same thing, unknown fields included.
			s := newStore(t)
			l := lock(t, s)
			if err := l.Write(m); err != nil {
				t.Fatalf("Write: %v", err)
			}
			written, err := os.ReadFile(s.Dir().Join(home.ManifestFile))
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(t, data, written) {
				t.Errorf("written back, the manifest changed meaning:\nbefore: %s\nafter:  %s", data, written)
			}
		})
	}
}

// sameJSON compares two JSON documents by meaning. Timestamps are compared as times, since a
// time may be spelled differently when written back.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatal(err)
	}
	return equalJSON(normalize(va), normalize(vb))
}

// normalize drops "version": 0, which the schema defines to mean the same as no version.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if n, ok := x["version"].(float64); ok && n == 0 {
			delete(x, "version")
		}
		for k, e := range x {
			x[k] = normalize(e)
		}
	case []any:
		for i := range x {
			x[i] = normalize(x[i])
		}
	}
	return v
}

func equalJSON(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			if k == "at" {
				ta, ea := time.Parse(time.RFC3339Nano, v.(string))
				tb, eb := time.Parse(time.RFC3339Nano, y[k].(string))
				if ea != nil || eb != nil || !ta.Equal(tb) {
					return false
				}
				continue
			}
			if !equalJSON(v, y[k]) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equalJSON(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	return a == b
}

func TestWriteKeepsThePreviousManifestAndReadsBack(t *testing.T) {
	s := newStore(t)
	l := lock(t, s)
	first := &Manifest{Entries: []Entry{sampleEntry("unit-testing")}}
	if err := l.Write(first); err != nil {
		t.Fatal(err)
	}
	firstBytes, _ := os.ReadFile(s.Dir().Join(home.ManifestFile))
	second := &Manifest{Entries: []Entry{sampleEntry("unit-testing"), sampleEntry("code-review")}}
	if err := l.Write(second); err != nil {
		t.Fatal(err)
	}
	prev, err := os.ReadFile(s.Dir().Join(home.ManifestPrevious))
	if err != nil || !bytes.Equal(prev, firstBytes) {
		t.Fatalf("the previous manifest was not kept as %s (%v)", home.ManifestPrevious, err)
	}
	m, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 2 || m.Entries[1].Resource != "code-review" {
		t.Fatalf("read back %+v", m.Entries)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(s.Dir().Join(home.ManifestFile))
		if fi.Mode().Perm() != home.FilePerm {
			t.Errorf("manifest mode %v, want %v", fi.Mode().Perm(), home.FilePerm)
		}
	}
}

func TestWriteRefusesAManifestItCouldNotReadBack(t *testing.T) {
	s := newStore(t)
	l := lock(t, s)
	bad := sampleEntry("unit-testing")
	bad.Digest = ""
	if err := l.Write(&Manifest{Entries: []Entry{bad}}); err == nil {
		t.Fatal("wrote a tree entry without a digest; the next run would find the manifest unknown")
	}
	if _, err := os.Stat(s.Dir().Join(home.ManifestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("something was written: %v", err)
	}
}

func TestASecondLockerIsTurnedAwayWithLockedAndUnknown(t *testing.T) {
	s := newStore(t)
	first := lock(t, s)
	start := time.Now()
	_, err := Open(s.Dir()).Lock(context.Background(), 150*time.Millisecond)
	if !errors.Is(err, ErrLocked) || !errors.Is(err, ErrUnknown) {
		t.Fatalf("second Lock = %v, want ErrLocked and ErrUnknown", err)
	}
	if waited := time.Since(start); waited < 150*time.Millisecond {
		t.Errorf("gave up after %v without waiting the 150ms it was given", waited)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := s.Lock(context.Background(), 0)
	if err != nil {
		t.Fatalf("Lock after release = %v", err)
	}
	second.Release()
	if _, err := first.Read(); err == nil {
		t.Error("a released lock still reads")
	}
}

func TestAWaitingLockerGetsTheLockWhenItIsReleased(t *testing.T) {
	s := newStore(t)
	first, err := s.Lock(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(100 * time.Millisecond)
		first.Release()
	}()
	l, err := s.Lock(context.Background(), 5*time.Second)
	<-released
	if err != nil {
		t.Fatalf("Lock = %v, want the lock once the holder let go", err)
	}
	l.Release()
}

func TestPendingIgnoresACutOffLastLineButNotABrokenOne(t *testing.T) {
	s := newStore(t)
	l := lock(t, s)
	in, err := l.Intend(Intent{Kind: Write, Entry: sampleEntry("unit-testing"), After: "sha256:x"})
	if err != nil {
		t.Fatal(err)
	}
	path := s.Dir().Join(home.JournalFile)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"id":"abc","kind":"wri`)
	f.Close()
	pending, err := l.Pending()
	if err != nil || len(pending) != 1 || pending[0].ID != in.ID {
		t.Fatalf("Pending = %v, %v; want the one complete intent", pending, err)
	}

	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\n")
	f.Close()
	if _, err := l.Pending(); !errors.Is(err, ErrUnknown) {
		t.Fatalf("Pending with a broken line in the middle = %v, want ErrUnknown", err)
	}
}

func TestRecoverCommitsDropsAndLeavesAlone(t *testing.T) {
	s := newStore(t)
	l := lock(t, s)
	done := sampleEntry("done")
	never := sampleEntry("never")
	other := sampleEntry("other")
	blind := sampleEntry("blind")
	gone := sampleEntry("gone")
	if err := l.Write(&Manifest{Entries: []Entry{gone}}); err != nil {
		t.Fatal(err)
	}
	for _, in := range []Intent{
		{Kind: Write, Entry: done, Before: "", After: done.Digest},
		{Kind: Write, Entry: never, Before: "", After: never.Digest},
		{Kind: Write, Entry: other, Before: "", After: other.Digest},
		{Kind: Write, Entry: blind, Before: "", After: blind.Digest},
		{Kind: Remove, Entry: gone, Before: gone.Digest, After: ""},
	} {
		if _, err := l.Intend(in); err != nil {
			t.Fatal(err)
		}
	}
	r, err := l.Recover(func(in Intent) Observation {
		switch in.Entry.Resource {
		case "done":
			return Observation{Exists: true, Digest: in.After}
		case "never", "gone":
			return Observation{}
		case "other":
			return Observation{Exists: true, Digest: "sha256:someone-else"}
		}
		return Observation{Err: errors.New("permission denied")}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Committed) != 2 || len(r.Dropped) != 1 || len(r.Unresolved) != 2 {
		t.Fatalf("Recover = %+v", r)
	}
	m, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 1 || m.Entries[0].Resource != "done" {
		t.Fatalf("manifest after recovery: %+v; want the completed write recorded and the completed removal dropped", m.Entries)
	}
	pending, _ := l.Pending()
	if len(pending) != 2 {
		t.Fatalf("%d intents left; the two that could not be settled must stay for a person to look at", len(pending))
	}
}

func TestTheSchemaAndTheGoTypesListTheSameFields(t *testing.T) {
	data, err := fs.ReadFile(schema.FS(), schema.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Properties map[string]any `json:"properties"`
		Defs       struct {
			Entry struct {
				Properties map[string]any `json:"properties"`
			} `json:"entry"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var edit struct {
		Defs struct {
			Entry struct {
				Properties struct {
					Edit struct {
						Properties map[string]any `json:"properties"`
					} `json:"edit"`
				} `json:"properties"`
			} `json:"entry"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &edit); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		schema map[string]any
		fields map[string]bool
	}{
		{"manifest", doc.Properties, fieldNames(&manifestJSON{})},
		{"entry", doc.Defs.Entry.Properties, fieldNames(&entryJSON{})},
		{"edit", edit.Defs.Entry.Properties.Edit.Properties, fieldNames(&keyEditJSON{})},
	} {
		for k := range c.schema {
			if !c.fields[k] {
				t.Errorf("%s: the schema has %q and the Go type does not; it would be carried along but never used", c.name, k)
			}
		}
		for k := range c.fields {
			if _, ok := c.schema[k]; !ok {
				t.Errorf("%s: the Go type has %q and the schema does not describe it", c.name, k)
			}
		}
	}
}

// An edit that only inserted text has no old bytes; it is written as "" and read back the same,
// because the schema allows no null there.
func TestAKeyEditWithoutOldBytesIsWrittenAndReadBack(t *testing.T) {
	s := newStore(t)
	l := lock(t, s)
	en := Entry{
		ID: EntryID("claude-code", OpKey, "~/.claude.json", []string{"mcpServers", "x"}), Tool: "claude-code", Resource: "jira",
		Op: OpKey, Path: "~/.claude.json", Format: "json", Key: []string{"mcpServers", "x"},
		Digest: "sha256:" + strings.Repeat("0", 64), Created: []string{"~/.claude.json"},
		Edit: &KeyEdit{Offset: 0, New: []byte("{\n  \"mcpServers\": {}\n}\n"), Created: 2},
		At:   time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC),
	}
	if err := l.Write(&Manifest{Schema: Version, Entries: []Entry{en}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	m, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	got := m.Entries[0].Edit
	if got == nil || got.Offset != 0 || len(got.Old) != 0 || string(got.New) != string(en.Edit.New) || got.Created != 2 {
		t.Fatalf("edit read back as %+v", got)
	}
}
