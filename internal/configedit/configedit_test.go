// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package configedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

// ours is the name of an entry this product writes.
var ours = brand.Prefix + "jira"

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// golden compares got with testdata/golden/name, or rewrites it with -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("result differs from %s:\n%s", path, lineDiff(want, got))
	}
}

// lineDiff shows the first differing line of two texts, with line endings made visible.
func lineDiff(want, got []byte) string {
	w := strings.SplitAfter(string(want), "\n")
	g := strings.SplitAfter(string(got), "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d:\n  want %q\n  got  %q", i+1, wl, gl)
		}
	}
	return "(identical lines; lengths differ)"
}

// checkSet asserts everything an edit by Set promises, independently of how Set checks itself.
func checkSet(t testing.TB, format adapter.Format, src, out []byte, e Edit, path []string, value any) {
	t.Helper()

	// One splice, everything around it untouched.
	if e.Offset < 0 || e.Offset+len(e.Old) > len(src) || e.Offset+len(e.New) > len(out) {
		t.Fatalf("edit offsets out of range: %+v", e)
	}
	if !bytes.Equal(out[:e.Offset], src[:e.Offset]) {
		t.Errorf("bytes before the edit changed")
	}
	if !bytes.Equal(out[e.Offset+len(e.New):], src[e.Offset+len(e.Old):]) {
		t.Errorf("bytes after the edit changed")
	}
	if !bytes.Equal(src[e.Offset:e.Offset+len(e.Old)], e.Old) || !bytes.Equal(out[e.Offset:e.Offset+len(e.New)], e.New) {
		t.Errorf("edit record does not describe the change")
	}

	// It parses, and the only difference in meaning is the key.
	before, err := Decode(format, src)
	if err != nil {
		t.Fatalf("Decode(src): %v", err)
	}
	after, err := Decode(format, out)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
	if format == adapter.JSON && len(src) > 0 && !json.Valid(bytes.TrimPrefix(out, utf8BOM)) {
		t.Errorf("result is not strict JSON")
	}
	want := deepCopy(before).(map[string]any)
	setIn(want, path, normalize(value))
	if !semanticEqual(format, after, want, path) {
		t.Errorf("meaning changed beyond the key:\n got  %v\n want %v", after, want)
	}
	got, ok, err := Get(format, out, path)
	if err != nil || !ok || !reflect.DeepEqual(got, normalize(value)) {
		t.Errorf("Get after Set = %v, %v, %v; want %v", got, ok, err, normalize(value))
	}

	// The file keeps its byte order mark, line endings and ending.
	if bytes.HasPrefix(src, utf8BOM) != bytes.HasPrefix(out, utf8BOM) {
		t.Errorf("byte order mark not preserved")
	}
	if bytes.Contains(src, []byte("\r\n")) && !hasLoneLF(src) && hasLoneLF(out) {
		t.Errorf("a CRLF file got LF line endings")
	}
	if len(bytes.TrimPrefix(src, utf8BOM)) > 0 && bytes.HasSuffix(src, []byte("\n")) != bytes.HasSuffix(out, []byte("\n")) {
		t.Errorf("the newline at the end of the file was added or removed")
	}

	// Undoing gives back the original, byte for byte, also from a stored record.
	for _, rec := range []Edit{e, roundTrip(t, e)} {
		back, how, err := Revert(out, rec)
		if err != nil {
			t.Fatalf("Revert: %v", err)
		}
		if how != ByteExact || !bytes.Equal(back, src) {
			t.Errorf("Revert = %v, not the original:\n%s", how, lineDiff(src, back))
		}
	}
}

func hasLoneLF(b []byte) bool {
	return bytes.Count(b, []byte("\n")) > bytes.Count(b, []byte("\r\n"))
}

// roundTrip stores and loads an edit record the way a manifest would.
func roundTrip(t testing.TB, e Edit) Edit {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var out Edit
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// mustSet calls Set and fails the test on an error.
func mustSet(t testing.TB, format adapter.Format, src []byte, path []string, value any) ([]byte, Edit) {
	t.Helper()
	out, e, err := Set(format, src, path, value)
	if err != nil {
		t.Fatalf("Set(%q): %v", path, err)
	}
	return out, e
}

func mustDecode(t testing.TB, format adapter.Format, src []byte) map[string]any {
	t.Helper()
	doc, err := Decode(format, src)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return doc
}

func TestSetRejectsInvalidRequests(t *testing.T) {
	src := fixture(t, "json/claude-user.json")
	path := []string{"mcpServers", ours}
	type unsupported struct{ A string }
	tests := []struct {
		name   string
		format adapter.Format
		path   []string
		value  any
	}{
		{"unknown format", "yaml", path, "x"},
		{"empty path", adapter.JSON, nil, "x"},
		{"empty key", adapter.JSON, []string{"mcpServers", ""}, "x"},
		{"nil value", adapter.JSON, path, nil},
		{"typed slice", adapter.JSON, path, []string{"a"}},
		{"struct", adapter.JSON, path, unsupported{"a"}},
		{"nested nil", adapter.JSON, path, map[string]any{"env": nil}},
		{"NaN", adapter.JSON, path, map[string]any{"n": nanValue()}},
		{"invalid UTF-8", adapter.JSON, path, "\xff"},
		{"TOML scalar", adapter.TOML, path, "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := Set(tt.format, src, tt.path, tt.value); !errors.Is(err, ErrInvalid) {
				t.Errorf("Set = %v, want ErrInvalid", err)
			}
		})
	}
}

func nanValue() float64 {
	zero := 0.0
	return zero / zero
}

func TestRevertRejectsMalformedRecords(t *testing.T) {
	src := fixture(t, "json/claude-user.json")
	out, e := mustSet(t, adapter.JSON, src, []string{"mcpServers", ours}, map[string]any{"type": "stdio"})
	for name, mutate := range map[string]func(*Edit){
		"no path":        func(e *Edit) { e.Path = nil },
		"no new bytes":   func(e *Edit) { e.New = nil },
		"created > path": func(e *Edit) { e.Created = 3 },
		"unknown format": func(e *Edit) { e.Format = "ini" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := e
			mutate(&bad)
			if _, _, err := Revert(out, bad); !errors.Is(err, ErrInvalid) {
				t.Errorf("Revert = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestUndoneString(t *testing.T) {
	for u, want := range map[Undone]string{ByteExact: "byte-exact", Semantic: "semantic", NothingToUndo: "nothing to undo", 0: "Undone(0)"} {
		if got := u.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", int(u), got, want)
		}
	}
}
