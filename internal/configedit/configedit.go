// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package configedit

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"reflect"
	"unicode/utf8"

	"github.com/code2rich/rigdepot/ext/adapter"
)

// Errors. Every error returned by this package wraps exactly one of these, so that the caller can
// tell what kind of failure it is with errors.Is.
var (
	// ErrSyntax: the file cannot be parsed in its format. Its content is unknown, so nothing may
	// be written to it.
	ErrSyntax = errors.New("configedit: the file cannot be parsed")

	// ErrUnwritable: the file parses, but the key cannot be written without rewriting something
	// the person wrote, for example because a parent is not an object or a table, or a key on
	// the path appears twice.
	ErrUnwritable = errors.New("configedit: the key cannot be written without rewriting the person's own entries")

	// ErrInlineTable: a TOML table on the path is an inline table (mcp_servers = { … }), which
	// TOML does not allow to be extended. It wraps ErrUnwritable.
	ErrInlineTable = fmt.Errorf("%w: a table on the path is an inline table", ErrUnwritable)

	// ErrInvalid: the request itself is wrong: an unknown format, an empty key path, or a value
	// of a type that is not allowed. This is a defect in the caller.
	ErrInvalid = errors.New("configedit: invalid request")

	// ErrNotFound: Delete was asked to remove a key that is not there.
	ErrNotFound = errors.New("configedit: the key is not there")

	// ErrSelfCheck: the edited bytes did not pass the check that the only difference is the
	// requested key. This is a defect in this package; nothing is returned to write.
	ErrSelfCheck = errors.New("configedit: the edited file failed its own check")
)

// Edit records one change made by Set so that it can be reversed. Offsets and lengths are in
// bytes of the whole file, including a byte order mark.
//
// The change is a single splice: in the file Set returned, New starts at Offset, and in the file
// Set was given, Old was at the same offset. Everything before Offset and everything after the
// splice is identical in both.
type Edit struct {
	Format adapter.Format `json:"format"`
	// Path is the key that was set.
	Path []string `json:"path"`
	// Offset is where New starts in the edited file, which is where Old started in the
	// original.
	Offset int `json:"offset"`
	// Old is the bytes that were replaced; empty when the edit only inserted text.
	Old []byte `json:"old"`
	// New is the bytes that took their place.
	New []byte `json:"new"`
	// Created is how many trailing elements of Path did not exist before the edit: 0 when an
	// existing key's value was replaced, 1 when the key was added to an existing parent, 2 when
	// its parent was created too, and so on. Undoing an insertion removes the parents it
	// created, if they are empty by then.
	Created int `json:"created"`
}

// Replaced reports whether the edit replaced the value of a key that already existed.
func (e Edit) Replaced() bool { return e.Created == 0 }

// Undone says how Revert undid an edit.
type Undone int

const (
	// ByteExact: the recorded bytes were put back; the rest of the file is untouched.
	ByteExact Undone = iota + 1
	// Semantic: the recorded bytes were no longer there, so the key was removed (or, for an edit
	// that replaced a value, the previous value restored) by editing the file as it is now. The
	// result is correct in meaning, not necessarily in bytes.
	Semantic
	// NothingToUndo: the key is not in the file any more. The file is returned unchanged.
	NothingToUndo
)

func (u Undone) String() string {
	switch u {
	case ByteExact:
		return "byte-exact"
	case Semantic:
		return "semantic"
	case NothingToUndo:
		return "nothing to undo"
	}
	return fmt.Sprintf("Undone(%d)", int(u))
}

// Set sets the key at path to value in src and returns the new file and the record that undoes
// it. Parents that do not exist are created. An empty src is a file that does not exist yet.
//
// value must be a string, bool, int, int64 or float64 (finite), or a []any or map[string]any of
// those, nested as needed. For TOML, value must be a map: it becomes a table.
func Set(format adapter.Format, src []byte, path []string, value any) ([]byte, Edit, error) {
	if err := checkPath(path); err != nil {
		return nil, Edit{}, err
	}
	if err := checkValue(value, 0); err != nil {
		return nil, Edit{}, err
	}
	var (
		sp      splice
		created int
		err     error
	)
	bom, body := splitBOM(src)
	switch format {
	case adapter.JSON, adapter.JSONC:
		sp, created, err = jsonSet(format, body, path, value)
	case adapter.TOML:
		sp, created, err = tomlSet(body, path, value)
	default:
		return nil, Edit{}, fmt.Errorf("%w: unknown format %q", ErrInvalid, format)
	}
	if err != nil {
		return nil, Edit{}, err
	}
	sp.offset += len(bom)
	out := sp.apply(src)

	// The self-check: the only difference is the key, with its value as requested.
	before, err := Decode(format, src)
	if err != nil {
		return nil, Edit{}, err
	}
	after, err := Decode(format, out)
	if err != nil {
		return nil, Edit{}, fmt.Errorf("%w: the result does not parse: %v", ErrSelfCheck, err)
	}
	want := deepCopy(before).(map[string]any)
	setIn(want, path, normalize(value))
	if !semanticEqual(format, after, want, path) {
		return nil, Edit{}, fmt.Errorf("%w: setting %q changed more than that key", ErrSelfCheck, path)
	}
	return out, Edit{
		Format:  format,
		Path:    append([]string(nil), path...),
		Offset:  sp.offset,
		Old:     sp.old,
		New:     sp.new,
		Created: created,
	}, nil
}

// Delete removes the key at path from src. Parents are left in place, even when they become
// empty. It returns ErrNotFound when the key is not there.
func Delete(format adapter.Format, src []byte, path []string) ([]byte, error) {
	if err := checkPath(path); err != nil {
		return nil, err
	}
	before, err := Decode(format, src)
	if err != nil {
		return nil, err
	}
	if _, ok := lookup(before, path); !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, path)
	}
	bom, body := splitBOM(src)
	var out []byte
	switch format {
	case adapter.JSON, adapter.JSONC:
		out, err = jsonDelete(body, path)
	case adapter.TOML:
		out, err = tomlDelete(body, path)
	}
	if err != nil {
		return nil, err
	}
	out = append(append([]byte(nil), bom...), out...)
	after, err := Decode(format, out)
	if err != nil {
		return nil, fmt.Errorf("%w: the result does not parse: %v", ErrSelfCheck, err)
	}
	want := deepCopy(before).(map[string]any)
	removeIn(want, path)
	if !semanticEqual(format, after, want, path) {
		return nil, fmt.Errorf("%w: deleting %q changed more than that key", ErrSelfCheck, path)
	}
	return out, nil
}

// Revert undoes e on cur, the file as it is now. See Undone for the three outcomes.
//
// For an edit that inserted a key, undoing removes the key and the parents the edit created, if
// they are empty by then. For an edit that replaced a value, undoing restores the previous value.
func Revert(cur []byte, e Edit) ([]byte, Undone, error) {
	if err := checkPath(e.Path); err != nil {
		return nil, 0, err
	}
	if e.Created < 0 || e.Created > len(e.Path) || len(e.New) == 0 {
		return nil, 0, fmt.Errorf("%w: malformed edit record", ErrInvalid)
	}
	doc, err := Decode(e.Format, cur)
	if err != nil {
		return nil, 0, err
	}
	if _, ok := lookup(doc, e.Path); !ok {
		return cur, NothingToUndo, nil
	}

	// What the file must mean after undoing.
	want := deepCopy(doc).(map[string]any)
	var previous any
	if e.Replaced() {
		previous, err = previousValue(e)
		if err != nil {
			return nil, 0, err
		}
		setIn(want, e.Path, normalize(previous))
	} else {
		removeIn(want, e.Path)
		pruneCreated(want, e.Path, e.Created)
	}

	// First choice: put the recorded bytes back where they were, or where they moved to when
	// something before them changed length.
	pos := -1
	if e.Offset >= 0 && e.Offset+len(e.New) <= len(cur) && bytes.Equal(cur[e.Offset:e.Offset+len(e.New)], e.New) {
		pos = e.Offset
	} else if bytes.Count(cur, e.New) == 1 {
		pos = bytes.Index(cur, e.New)
	}
	if pos >= 0 {
		candidate := splice{offset: pos, old: e.New, new: e.Old}.apply(cur)
		if got, err := Decode(e.Format, candidate); err == nil && semanticEqual(e.Format, got, want, e.Path) {
			return candidate, ByteExact, nil
		}
	}

	// Otherwise edit the file as it is now.
	var out []byte
	if e.Replaced() {
		out, _, err = Set(e.Format, cur, e.Path, previous)
	} else {
		out, err = Delete(e.Format, cur, e.Path)
		for i := len(e.Path) - 1; err == nil && i > len(e.Path)-e.Created; i-- {
			out, err = deleteIfEmpty(e.Format, out, e.Path[:i])
		}
	}
	if err != nil {
		return nil, 0, err
	}
	got, err := Decode(e.Format, out)
	if err != nil || !semanticEqual(e.Format, got, want, e.Path) {
		return nil, 0, fmt.Errorf("%w: undoing %q changed more than that key", ErrSelfCheck, e.Path)
	}
	return out, Semantic, nil
}

// Get returns the value of the key at path in src, normalised as Decode describes, and whether
// it is there.
func Get(format adapter.Format, src []byte, path []string) (any, bool, error) {
	if err := checkPath(path); err != nil {
		return nil, false, err
	}
	doc, err := Decode(format, src)
	if err != nil {
		return nil, false, err
	}
	v, ok := lookup(doc, path)
	return v, ok, nil
}

// Decode parses src and returns its meaning: objects and tables as map[string]any, arrays as
// []any, numbers as int64 when they are integers that fit and float64 otherwise, and strings and
// booleans as themselves. TOML dates and times keep go-toml's types. An empty src is an empty
// document. A file that does not parse, or whose top level is not an object, is ErrSyntax.
func Decode(format adapter.Format, src []byte) (map[string]any, error) {
	_, body := splitBOM(src)
	switch format {
	case adapter.JSON, adapter.JSONC:
		return jsonDecode(format, body)
	case adapter.TOML:
		return tomlDecode(body)
	}
	return nil, fmt.Errorf("%w: unknown format %q", ErrInvalid, format)
}

// deleteIfEmpty deletes the object at path if it is there and empty.
func deleteIfEmpty(format adapter.Format, src []byte, path []string) ([]byte, error) {
	v, ok, err := Get(format, src, path)
	if err != nil || !ok {
		return src, err
	}
	if m, isMap := v.(map[string]any); !isMap || len(m) > 0 {
		return src, nil
	}
	if format == adapter.TOML {
		// An empty TOML table with its own header is the person's; ours are implicit and
		// disappear with their last child.
		return src, nil
	}
	return Delete(format, src, path)
}

// previousValue decodes the value an edit replaced from the bytes it recorded.
func previousValue(e Edit) (any, error) {
	var v any
	switch e.Format {
	case adapter.JSON, adapter.JSONC:
		doc, err := jsonDecode(adapter.JSONC, append(append([]byte(`{"v":`), e.Old...), '}'))
		if err != nil {
			return nil, fmt.Errorf("%w: the recorded previous value does not parse", ErrInvalid)
		}
		v = doc["v"]
	case adapter.TOML:
		doc, err := tomlDecode(e.Old)
		if err != nil {
			return nil, fmt.Errorf("%w: the recorded previous value does not parse", ErrInvalid)
		}
		var ok bool
		if v, ok = lookup(doc, e.Path); !ok {
			return nil, fmt.Errorf("%w: the recorded previous value is not at the key", ErrInvalid)
		}
	default:
		return nil, fmt.Errorf("%w: unknown format %q", ErrInvalid, e.Format)
	}
	if err := checkValue(v, 0); err != nil {
		return nil, err
	}
	return v, nil
}

// splice replaces old, which starts at offset, with new.
type splice struct {
	offset   int
	old, new []byte
}

func (s splice) apply(b []byte) []byte {
	out := make([]byte, 0, len(b)-len(s.old)+len(s.new))
	out = append(out, b[:s.offset]...)
	out = append(out, s.new...)
	return append(out, b[s.offset+len(s.old):]...)
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func splitBOM(src []byte) (bom, body []byte) {
	if bytes.HasPrefix(src, utf8BOM) {
		return src[:len(utf8BOM)], src[len(utf8BOM):]
	}
	return nil, src
}

// newline returns the line ending the file mostly uses: CRLF when at least half of its line
// endings are CRLF, LF otherwise (and for a file with no line ending at all).
func newline(b []byte) string {
	crlf := bytes.Count(b, []byte("\r\n"))
	lf := bytes.Count(b, []byte("\n"))
	if crlf > 0 && 2*crlf >= lf {
		return "\r\n"
	}
	return "\n"
}

const maxDepth = 64

func checkPath(path []string) error {
	if len(path) == 0 || len(path) > maxDepth {
		return fmt.Errorf("%w: key path of length %d", ErrInvalid, len(path))
	}
	for _, p := range path {
		if p == "" || !utf8.ValidString(p) {
			return fmt.Errorf("%w: key path element %q", ErrInvalid, p)
		}
	}
	return nil
}

// checkValue enforces the value types adapter.SetKey documents.
func checkValue(v any, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("%w: value nested more than %d levels", ErrInvalid, maxDepth)
	}
	switch v := v.(type) {
	case string:
		if !utf8.ValidString(v) {
			return fmt.Errorf("%w: string value is not valid UTF-8", ErrInvalid)
		}
	case bool, int, int64:
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%w: number %v cannot be written", ErrInvalid, v)
		}
	case []any:
		for _, e := range v {
			if err := checkValue(e, depth+1); err != nil {
				return err
			}
		}
	case map[string]any:
		for k, e := range v {
			if !utf8.ValidString(k) {
				return fmt.Errorf("%w: key %q is not valid UTF-8", ErrInvalid, k)
			}
			if err := checkValue(e, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: value of type %T", ErrInvalid, v)
	}
	return nil
}

// normalize converts a value to the form Decode returns, so that the two can be compared.
func normalize(v any) any {
	switch v := v.(type) {
	case int:
		return int64(v)
	case float64:
		return normalFloat(v)
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = normalize(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = normalize(e)
		}
		return out
	}
	return v
}

// normalFloat returns an integral float as an int64, so that 5 and 5.0 compare equal whichever
// way a file spells them.
func normalFloat(f float64) any {
	if f == math.Trunc(f) && f >= -(1<<63) && f < 1<<63 {
		return int64(f)
	}
	return f
}

func deepCopy(v any) any {
	switch v := v.(type) {
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = deepCopy(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = deepCopy(e)
		}
		return out
	}
	return v
}

func lookup(doc map[string]any, path []string) (any, bool) {
	var cur any = doc
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// setIn sets path in doc, creating maps on the way.
func setIn(doc map[string]any, path []string, v any) {
	m := doc
	for _, p := range path[:len(path)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	m[path[len(path)-1]] = v
}

func removeIn(doc map[string]any, path []string) {
	parent, ok := lookup(doc, path[:len(path)-1])
	if m, isMap := parent.(map[string]any); ok && isMap {
		delete(m, path[len(path)-1])
	}
}

// pruneCreated removes the parents of path that an insertion created, deepest first, as long as
// they are empty.
func pruneCreated(doc map[string]any, path []string, created int) {
	for i := len(path) - 1; i > len(path)-created; i-- {
		v, ok := lookup(doc, path[:i])
		if m, isMap := v.(map[string]any); ok && isMap && len(m) == 0 {
			removeIn(doc, path[:i])
		}
	}
}

// semanticEqual compares two decoded documents. In TOML a table that exists only because it has
// a sub-table disappears with its last sub-table, so empty tables along path are not a
// difference there.
func semanticEqual(format adapter.Format, a, b map[string]any, path []string) bool {
	if format == adapter.TOML {
		a = deepCopy(a).(map[string]any)
		b = deepCopy(b).(map[string]any)
		dropEmptyAlong(a, path)
		dropEmptyAlong(b, path)
	}
	return reflect.DeepEqual(a, b)
}

func dropEmptyAlong(doc map[string]any, path []string) {
	for i := len(path); i >= 1; i-- {
		v, ok := lookup(doc, path[:i])
		if m, isMap := v.(map[string]any); ok && isMap && len(m) == 0 {
			removeIn(doc, path[:i])
		}
	}
}
