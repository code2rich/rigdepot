// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package configedit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/code2rich/rigdepot/ext/adapter"
)

// jsonDecode parses a JSON or JSONC body (without a byte order mark).
func jsonDecode(format adapter.Format, body []byte) (map[string]any, error) {
	if len(body) == 0 {
		return map[string]any{}, nil
	}
	if format == adapter.JSON && !json.Valid(body) {
		return nil, fmt.Errorf("%w: not valid JSON", ErrSyntax)
	}
	v, err := hujson.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSyntax, err)
	}
	if v.Value.Kind() != '{' {
		return nil, fmt.Errorf("%w: the top level is not an object", ErrSyntax)
	}
	v = v.Clone()
	v.Standardize()
	dec := json.NewDecoder(bytes.NewReader(v.Pack()))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSyntax, err)
	}
	return normalizeJSON(doc).(map[string]any), nil
}

func normalizeJSON(v any) any {
	switch v := v.(type) {
	case json.Number:
		if i, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(string(v), 64); err == nil {
			return normalFloat(f)
		}
		return v // out of range: compared by its spelling
	case []any:
		for i, e := range v {
			v[i] = normalizeJSON(e)
		}
	case map[string]any:
		for k, e := range v {
			v[k] = normalizeJSON(e)
		}
	}
	return v
}

// jsonStyle is how the surrounding text is written, so that what we add looks like it.
type jsonStyle struct {
	nl    string // line ending
	unit  string // one level of indentation
	colon string // ":" or ": "
	comma string // "," or ", " between members on one line
}

// jsonSet computes the splice that sets path to value in body.
func jsonSet(format adapter.Format, body []byte, path []string, value any) (splice, int, error) {
	if len(body) == 0 {
		// A new file, written the way the tools write theirs.
		st := jsonStyle{nl: "\n", unit: "  ", colon: ": ", comma: ", "}
		text := "{" + st.nl + st.unit + quoteJSON(path[0]) + st.colon +
			renderJSON(nest(path[1:], value), st.unit, st) + st.nl + "}" + st.nl
		return splice{offset: 0, new: []byte(text)}, len(path), nil
	}
	if _, err := jsonDecode(format, body); err != nil {
		return splice{}, 0, err
	}
	root, err := hujson.Parse(body)
	if err != nil {
		return splice{}, 0, fmt.Errorf("%w: %v", ErrSyntax, err)
	}
	st := docStyle(&root, newline(body), jsonUnit(body, &root))
	cur := &root
	for i, seg := range path {
		obj := cur.Value.(*hujson.Object)
		idx, err := findMember(obj, path[:i+1])
		if err != nil {
			return splice{}, 0, err
		}
		if idx < 0 {
			return jsonInsert(body, cur, seg, nest(path[i+1:], value), st), len(path) - i, nil
		}
		m := &obj.Members[idx]
		if i == len(path)-1 {
			indent, own := lineIndent(body, m.Name.StartOffset)
			text := renderJSONInline(value, st)
			if own {
				text = renderJSON(value, indent, st)
			}
			return splice{
				offset: m.Value.StartOffset,
				old:    body[m.Value.StartOffset:m.Value.EndOffset],
				new:    []byte(text),
			}, 0, nil
		}
		if m.Value.Value.Kind() != '{' {
			return splice{}, 0, fmt.Errorf("%w: %q is not an object", ErrUnwritable, path[:i+1])
		}
		cur = &m.Value
	}
	panic("unreachable")
}

// jsonInsert adds the member key: value at the end of the object v.
func jsonInsert(body []byte, v *hujson.Value, key string, value any, st jsonStyle) splice {
	obj := v.Value.(*hujson.Object)
	open, closing := v.StartOffset, v.EndOffset-1
	parentIndent := leadingSpace(body, open)
	nl := st.nl

	if len(obj.Members) == 0 {
		indent := parentIndent + st.unit
		member := quoteJSON(key) + st.colon + renderJSON(value, indent, st)
		interior := body[open+1 : closing]
		switch {
		case bytes.ContainsRune(interior, '/'):
			// Keep the comments that are already inside the empty object.
			if ind, own := lineIndent(body, closing); own {
				return splice{offset: closing - len(ind), new: []byte(indent + member + nl)}
			}
			return splice{offset: closing, new: []byte(nl + indent + member + nl + parentIndent)}
		case bytes.ContainsRune(body, '\n'):
			return splice{offset: open + 1, old: interior, new: []byte(nl + indent + member + nl + parentIndent)}
		default:
			return splice{offset: open + 1, old: interior, new: []byte(quoteJSON(key) + st.colon + renderJSONInline(value, st))}
		}
	}

	last := &obj.Members[len(obj.Members)-1]
	anchor, prefix, suffix := last.Value.EndOffset, ",", ""
	if last.Value.AfterExtra != nil { // trailing comma: keep the habit
		anchor, prefix, suffix = last.Value.EndOffset+len(last.Value.AfterExtra)+1, "", ","
	}
	indent, own := lineIndent(body, last.Name.StartOffset)
	if !own {
		// The object is written on one line: stay on it.
		sep := strings.TrimPrefix(st.comma, ",")
		member := quoteJSON(key) + st.colon + renderJSONInline(value, st)
		return splice{offset: anchor, new: []byte(prefix + sep + member + suffix)}
	}
	if strings.HasPrefix(indent, parentIndent) && len(indent) > len(parentIndent) {
		st.unit = indent[len(parentIndent):]
	}
	member := quoteJSON(key) + st.colon + renderJSON(value, indent, st)
	rest := body[anchor:closing]
	if i := bytes.IndexByte(rest, '\n'); i >= 0 && !bytes.Contains(rest[:i], []byte("/*")) {
		// Whatever follows the last member on its line (a comment) stays with it; the new
		// member gets a line of its own after it.
		old := rest[:i+1]
		return splice{offset: anchor, old: old, new: []byte(prefix + string(old) + indent + member + suffix + nl)}
	}
	return splice{offset: anchor, new: []byte(prefix + nl + indent + member + suffix)}
}

// jsonDelete removes the member at path from body.
func jsonDelete(body []byte, path []string) ([]byte, error) {
	root, err := hujson.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSyntax, err)
	}
	cur := &root
	for i := range path {
		obj := cur.Value.(*hujson.Object)
		idx, err := findMember(obj, path[:i+1])
		if err != nil {
			return nil, err
		}
		if idx < 0 {
			return nil, fmt.Errorf("%w: %q", ErrNotFound, path)
		}
		if i < len(path)-1 {
			cur = &obj.Members[idx].Value
			if cur.Value.Kind() != '{' {
				return nil, fmt.Errorf("%w: %q", ErrNotFound, path)
			}
			continue
		}
		return removeMember(body, cur, idx), nil
	}
	panic("unreachable")
}

// removeMember returns body without member i of the object v, and without the line it was on
// when it had a line of its own.
func removeMember(body []byte, v *hujson.Value, i int) []byte {
	obj := v.Value.(*hujson.Object)
	n := len(obj.Members)
	m := &obj.Members[i]
	start, valueEnd := m.Name.StartOffset, m.Value.EndOffset
	_, own := lineIndent(body, start)
	cut := func(ranges ...[2]int) []byte { // ranges in ascending order, not overlapping
		out := make([]byte, 0, len(body))
		prev := 0
		for _, r := range ranges {
			out = append(out, body[prev:r[0]]...)
			prev = r[1]
		}
		return append(out, body[prev:]...)
	}

	if i < n-1 || m.Value.AfterExtra != nil {
		// A comma follows the member: remove up to and including it.
		end := valueEnd + len(m.Value.AfterExtra) + 1
		for end < len(body) && (body[end] == ' ' || body[end] == '\t') {
			end++
		}
		if own {
			if j := lineEndAt(body, end); j >= 0 {
				return cut([2]int{lineStart(body, start), j})
			}
		}
		return cut([2]int{start, end})
	}
	if n == 1 {
		open, closing := v.StartOffset, v.EndOffset-1
		rest := slices.Concat(body[open+1:start], body[valueEnd:closing])
		if !bytes.ContainsRune(rest, '/') {
			return cut([2]int{open + 1, closing}) // nothing else in there: "{}"
		}
		return cut([2]int{start, valueEnd})
	}
	// The last member, without a trailing comma: the comma after the previous one goes too.
	prev := &obj.Members[i-1]
	comma := prev.Value.EndOffset + len(prev.Value.AfterExtra)
	if own {
		ls := lineStart(body, start)
		from := ls - 1
		if from > 0 && body[from-1] == '\r' {
			from--
		}
		if from > comma {
			return cut([2]int{comma, comma + 1}, [2]int{from, valueEnd})
		}
	}
	if !bytes.ContainsRune(body[comma+1:start], '/') {
		return cut([2]int{comma, valueEnd})
	}
	return cut([2]int{comma, comma + 1}, [2]int{start, valueEnd})
}

// findMember returns the index of the member named path[len(path)-1] in obj, or -1. A name that
// appears more than once makes the key ambiguous.
func findMember(obj *hujson.Object, path []string) (int, error) {
	name := path[len(path)-1]
	idx := -1
	for i := range obj.Members {
		lit, ok := obj.Members[i].Name.Value.(hujson.Literal)
		if !ok || lit.String() != name {
			continue
		}
		if idx >= 0 {
			return -1, fmt.Errorf("%w: %q appears more than once", ErrUnwritable, path)
		}
		idx = i
	}
	return idx, nil
}

// docStyle looks at how the members of the document's objects are written: with or without a
// space after the colon, and after a comma between members on the same line. Most of the
// document wins; with no evidence, the spaced form.
func docStyle(root *hujson.Value, nl, unit string) jsonStyle {
	var colon, tight, comma, tightComma int
	for v := range root.All() {
		obj, ok := v.Value.(*hujson.Object)
		if !ok {
			continue
		}
		for i := range obj.Members {
			m := &obj.Members[i]
			if len(m.Name.AfterExtra) == 0 && len(m.Value.BeforeExtra) == 0 {
				tight++
			} else {
				colon++
			}
			if i > 0 && !bytes.ContainsRune(m.Name.BeforeExtra, '\n') {
				if len(m.Name.BeforeExtra) == 0 {
					tightComma++
				} else {
					comma++
				}
			}
		}
	}
	st := jsonStyle{nl: nl, unit: unit, colon: ": ", comma: ", "}
	if tight > colon {
		st.colon = ":"
	}
	if tightComma > comma {
		st.comma = ","
	}
	return st
}

// jsonUnit is the indentation of one level in the document: the indentation of the top-level
// members, if they are on lines of their own.
func jsonUnit(body []byte, root *hujson.Value) string {
	obj := root.Value.(*hujson.Object)
	if len(obj.Members) > 0 {
		ind, own := lineIndent(body, obj.Members[0].Name.StartOffset)
		base := leadingSpace(body, root.StartOffset)
		if own && strings.HasPrefix(ind, base) && len(ind) > len(base) {
			return ind[len(base):]
		}
	}
	return "  "
}

// lineIndent returns the text between the start of the line and pos, and whether that text is
// only spaces and tabs. When it is not, it returns the line's leading spaces and tabs.
func lineIndent(body []byte, pos int) (string, bool) {
	ls := lineStart(body, pos)
	s := body[ls:pos]
	for i, c := range s {
		if c != ' ' && c != '\t' {
			return string(s[:i]), false
		}
	}
	return string(s), true
}

func leadingSpace(body []byte, pos int) string {
	ind, _ := lineIndent(body, pos)
	return ind
}

func lineStart(body []byte, pos int) int {
	return bytes.LastIndexByte(body[:pos], '\n') + 1
}

// lineEndAt returns the offset just after the line ending at pos, or -1 when pos is not at a
// line ending.
func lineEndAt(body []byte, pos int) int {
	switch {
	case pos < len(body) && body[pos] == '\n':
		return pos + 1
	case pos+1 < len(body) && body[pos] == '\r' && body[pos+1] == '\n':
		return pos + 2
	}
	return -1
}

// nest wraps value in one object per element of path.
func nest(path []string, value any) any {
	for i := len(path) - 1; i >= 0; i-- {
		value = map[string]any{path[i]: value}
	}
	return value
}

// renderJSON writes a value over several lines, like JSON.stringify with indentation: indent is
// the indentation of the line the value starts on.
func renderJSON(v any, indent string, st jsonStyle) string {
	inner := indent + st.unit
	switch v := v.(type) {
	case map[string]any:
		if len(v) == 0 {
			return "{}"
		}
		var b strings.Builder
		b.WriteString("{")
		for i, k := range sortedKeys(v) {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(st.nl + inner + quoteJSON(k) + st.colon + renderJSON(v[k], inner, st))
		}
		b.WriteString(st.nl + indent + "}")
		return b.String()
	case []any:
		if len(v) == 0 {
			return "[]"
		}
		var b strings.Builder
		b.WriteString("[")
		for i, e := range v {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(st.nl + inner + renderJSON(e, inner, st))
		}
		b.WriteString(st.nl + indent + "]")
		return b.String()
	}
	return scalarJSON(v)
}

// renderJSONInline writes a value on one line.
func renderJSONInline(v any, st jsonStyle) string {
	switch v := v.(type) {
	case map[string]any:
		parts := make([]string, 0, len(v))
		for _, k := range sortedKeys(v) {
			parts = append(parts, quoteJSON(k)+st.colon+renderJSONInline(v[k], st))
		}
		return "{" + strings.Join(parts, st.comma) + "}"
	case []any:
		parts := make([]string, 0, len(v))
		for _, e := range v {
			parts = append(parts, renderJSONInline(e, st))
		}
		return "[" + strings.Join(parts, st.comma) + "]"
	}
	return scalarJSON(v)
}

func scalarJSON(v any) string {
	switch v := v.(type) {
	case string:
		return quoteJSON(v)
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		if v == math.Trunc(v) && math.Abs(v) < 1e21 {
			// Spell an integral float out exactly. The shortest spelling json.Marshal picks
			// (100000000000000020 for 1.0000000000000002e17) reads back as the same float, but as
			// a different integer to a reader that keeps integers as integers.
			return strconv.FormatFloat(v, 'f', 0, 64)
		}
		b, err := json.Marshal(v)
		if err != nil {
			panic(err) // checkValue rejected NaN and infinities
		}
		return string(b)
	}
	panic(fmt.Sprintf("configedit: unchecked value of type %T", v))
}

// quoteJSON writes a JSON string without escaping <, > and &, which encoding/json would turn
// into < and friends.
func quoteJSON(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		panic(err) // a string always encodes
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
