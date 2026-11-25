// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package configedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
)

// The fuzz targets check the two properties every edit promises:
//
//  1. Set and then Revert gives back the original file, byte for byte.
//  2. The file Set returns parses, and its meaning differs from the original's in exactly the one
//     key (plus the parents it had to create).
//
// go test runs them on the seeds below (the fixtures under testdata) and on whatever is in
// testdata/fuzz; a longer run is
//
//	go test ./internal/configedit -run '^$' -fuzz FuzzSetRevert -fuzztime 10m

var fuzzFormats = []adapter.Format{adapter.JSON, adapter.JSONC}

func formatIndex(f adapter.Format) uint8 {
	for i, g := range fuzzFormats {
		if g == f {
			return uint8(i)
		}
	}
	panic(f)
}

// fuzzSeeds adds every fixture with a few paths and values.
func fuzzSeeds(f *testing.F, add func(format adapter.Format, src []byte, path, value string)) {
	paths := map[adapter.Format][]string{
		adapter.JSON:  {"mcpServers/" + ours, "projects//home/example/work/app/mcpServers/" + ours, "a"},
		adapter.JSONC: {"mcp/servers/" + ours, "mcp/" + ours, "a/b/c"},
	}
	values := []string{
		`{"type":"stdio","command":"/opt/example/bin/tool","args":["mcp","run"],"env":{}}`,
		`{"url":"https://mcp.example.com","headers":{"X-Token":"<placeholder: token>"},"n":[1,2.5,-0,1e21,{"k":true}]}`,
		`"x"`,
		`{}`,
	}
	for _, dir := range []string{"json"} {
		files, err := filepath.Glob(filepath.Join("testdata", dir, "*"))
		if err != nil {
			f.Fatal(err)
		}
		for _, file := range files {
			src, err := os.ReadFile(file)
			if err != nil {
				f.Fatal(err)
			}
			formats := []adapter.Format{adapter.JSON, adapter.JSONC}
			for _, format := range formats {
				for _, p := range paths[format] {
					for _, v := range values {
						add(format, src, p, v)
					}
				}
			}
		}
	}
}

// fuzzValue turns JSON text into a value Set accepts, or reports that it cannot.
func fuzzValue(text string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, false
	}
	return convertFuzzValue(v)
}

func convertFuzzValue(v any) (any, bool) {
	switch v := v.(type) {
	case nil:
		return nil, false
	case json.Number:
		if i, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return i, true
		}
		f, err := strconv.ParseFloat(string(v), 64)
		return f, err == nil
	case []any:
		for i, e := range v {
			var ok bool
			if v[i], ok = convertFuzzValue(e); !ok {
				return nil, false
			}
		}
	case map[string]any:
		for k, e := range v {
			var ok bool
			if v[k], ok = convertFuzzValue(e); !ok {
				return nil, false
			}
		}
	}
	return v, true
}

func FuzzSetRevert(f *testing.F) {
	fuzzSeeds(f, func(format adapter.Format, src []byte, path, value string) {
		f.Add(formatIndex(format), src, path, value)
	})
	f.Fuzz(func(t *testing.T, kind uint8, src []byte, pathSpec, valueText string) {
		format := fuzzFormats[int(kind)%len(fuzzFormats)]
		path := strings.Split(pathSpec, "/")
		value, ok := fuzzValue(valueText)
		if !ok {
			return
		}

		out, e, err := Set(format, src, path, value)
		if err != nil {
			switch {
			case errors.Is(err, ErrSelfCheck):
				t.Fatalf("Set produced a result that failed its own check: %v", err)
			case errors.Is(err, ErrSyntax):
				if _, derr := Decode(format, src); derr == nil {
					t.Fatalf("Set reported a syntax error for a file that parses: %v", err)
				}
			case errors.Is(err, ErrUnwritable), errors.Is(err, ErrInvalid):
			default:
				t.Fatalf("Set returned an error of no known kind: %v", err)
			}
			if out != nil {
				t.Fatalf("Set returned bytes along with an error")
			}
			return
		}

		// Both properties, and that the rest of the file is untouched.
		checkSet(t, format, src, out, e, path, value)

		// Deleting the key again works on everything Set wrote.
		removed, err := Delete(format, out, path)
		if err != nil {
			t.Fatalf("Delete after Set: %v", err)
		}
		want := mustDecode(t, format, out)
		removeIn(want, path)
		if got := mustDecode(t, format, removed); !semanticEqual(format, got, want, path) {
			t.Fatalf("Delete after Set changed more than the key")
		}
	})
}

func FuzzDelete(f *testing.F) {
	fuzzSeeds(f, func(format adapter.Format, src []byte, path, _ string) {
		f.Add(formatIndex(format), src, path)
	})
	// Seeds aimed at the layout cases of removing a member.
	for _, s := range []string{
		"{\n  \"a\": 1, // about a\n  \"k\": 2 // about k\n}",
		"{\"a\":1 /* c */, \"k\": {\"x\": [1, {}]} , }",
		"{\r\n\t\"k\": 2,\r\n\t\"b\": 3,\r\n}\r\n",
		"{ \"k\" : 2 }",
	} {
		f.Add(formatIndex(adapter.JSONC), []byte(s), "k")
	}
	f.Fuzz(func(t *testing.T, kind uint8, src []byte, pathSpec string) {
		format := fuzzFormats[int(kind)%len(fuzzFormats)]
		path := strings.Split(pathSpec, "/")
		if checkPath(path) != nil {
			return
		}
		doc, err := Decode(format, src)
		if err != nil {
			if _, derr := Delete(format, src, path); !errors.Is(derr, ErrSyntax) && !errors.Is(derr, ErrInvalid) {
				t.Fatalf("Delete on a file that does not parse = %v", derr)
			}
			return
		}
		out, err := Delete(format, src, path)
		if _, present := lookup(doc, path); !present {
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("Delete of a missing key = %v, want ErrNotFound", err)
			}
			return
		}
		if err != nil {
			if errors.Is(err, ErrUnwritable) {
				return // a duplicated key
			}
			t.Fatalf("Delete: %v", err)
		}
		want := deepCopy(doc).(map[string]any)
		removeIn(want, path)
		if got := mustDecode(t, format, out); !semanticEqual(format, got, want, path) {
			t.Fatalf("Delete changed more than the key")
		}
		if bytes.HasPrefix(src, utf8BOM) != bytes.HasPrefix(out, utf8BOM) {
			t.Fatalf("Delete lost or added the byte order mark")
		}
	})
}
