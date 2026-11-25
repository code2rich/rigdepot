// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package configedit

import (
	"bytes"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// tomlNaN stands for a NaN in a decoded document, so that a file containing one compares equal
// to itself.
type tomlNaN struct{}

// tomlDecode parses a TOML body (without a byte order mark).
func tomlDecode(body []byte) (map[string]any, error) {
	doc := map[string]any{}
	if err := toml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSyntax, err)
	}
	return normalizeTOML(doc).(map[string]any), nil
}

func normalizeTOML(v any) any {
	switch v := v.(type) {
	case float64:
		if math.IsNaN(v) {
			return tomlNaN{}
		}
		return normalFloat(v)
	case []any:
		for i, e := range v {
			v[i] = normalizeTOML(e)
		}
	case map[string]any:
		for k, e := range v {
			v[k] = normalizeTOML(e)
		}
	}
	return v
}

type exprKind int

const (
	exprComment exprKind = iota
	exprKeyValue
	exprTable
	exprArrayTable
)

// tomlExpr is one top-level line (or lines, for a multi-line string) of a TOML file.
type tomlExpr struct {
	kind exprKind
	// key is the full key: the header's for a table, the header's followed by the dotted key
	// for a key-value.
	key []string
	// inArray: a key-value inside an array of tables, whose full key does not name one place.
	inArray bool
	// value is the kind of a key-value's value.
	value unstable.Kind
	// start is the start of the expression's first line; end is just after its last line's
	// line ending (or the end of the file).
	start, end int
}

func (e tomlExpr) isHeader() bool { return e.kind == exprTable || e.kind == exprArrayTable }

// tomlScan lists the top-level expressions of body with their positions.
func tomlScan(body []byte) ([]tomlExpr, error) {
	p := unstable.Parser{KeepComments: true}
	p.Reset(body)
	var (
		out     []tomlExpr
		header  []string
		inArray bool
	)
	keyOf := func(n *unstable.Node) (parts []string, first, last unstable.Range) {
		it := n.Key()
		for i := 0; it.Next(); i++ {
			k := it.Node()
			if i == 0 {
				first = k.Raw
			}
			last = k.Raw
			parts = append(parts, string(k.Data))
		}
		return parts, first, last
	}
	for p.NextExpression() {
		n := p.Expression()
		switch n.Kind {
		case unstable.Comment:
			start := int(n.Raw.Offset)
			out = append(out, tomlExpr{kind: exprComment, start: lineStart(body, start), end: lineEnd(body, start)})
		case unstable.KeyValue:
			key, _, _ := keyOf(n)
			out = append(out, tomlExpr{
				kind:    exprKeyValue,
				key:     slices.Concat(header, key),
				inArray: inArray,
				value:   n.Value().Kind,
				start:   lineStart(body, int(n.Raw.Offset)),
				end:     lineEnd(body, int(n.Raw.Offset+n.Raw.Length)),
			})
		case unstable.Table, unstable.ArrayTable:
			key, first, last := keyOf(n)
			header, inArray = key, n.Kind == unstable.ArrayTable
			kind := exprTable
			if inArray {
				kind = exprArrayTable
			}
			out = append(out, tomlExpr{
				kind:  kind,
				key:   key,
				start: lineStart(body, int(first.Offset)),
				end:   lineEnd(body, int(last.Offset+last.Length)),
			})
		}
	}
	if err := p.Error(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSyntax, err)
	}
	return out, nil
}

// lineEnd returns the offset just after the line ending that follows pos, or the end of body.
func lineEnd(body []byte, pos int) int {
	if i := bytes.IndexByte(body[pos:], '\n'); i >= 0 {
		return pos + i + 1
	}
	return len(body)
}

// hasPrefix reports whether key starts with prefix.
func hasPrefix(key, prefix []string) bool {
	return len(key) >= len(prefix) && slices.Equal(key[:len(prefix)], prefix)
}

// tomlCheckPath rejects a path that runs through something other than a table written with
// headers: an inline table, an array of tables, or a value.
func tomlCheckPath(exprs []tomlExpr, path []string) error {
	for _, e := range exprs {
		switch {
		case e.kind == exprKeyValue && !e.inArray && len(e.key) < len(path) && hasPrefix(path, e.key):
			if e.value == unstable.InlineTable {
				return fmt.Errorf("%w: %q", ErrInlineTable, e.key)
			}
			return fmt.Errorf("%w: %q is not a table", ErrUnwritable, e.key)
		case e.kind == exprArrayTable && hasPrefix(path, e.key):
			return fmt.Errorf("%w: %q is an array of tables", ErrUnwritable, e.key)
		}
	}
	return nil
}

// tomlBlock finds the table at path, written as a header "[path]" followed by its keys and its
// sub-tables, all in one run of lines. It returns where the run starts and where its last
// key-value or header line ends; comments between that and the next table stay outside.
func tomlBlock(exprs []tomlExpr, path []string) (start, end int, err error) {
	h := -1
	for i, e := range exprs {
		if e.kind == exprTable && slices.Equal(e.key, path) {
			h = i
			break
		}
	}
	if h < 0 {
		return 0, 0, fmt.Errorf("%w: %q is not written as a [table] of its own", ErrUnwritable, path)
	}
	j, end := h+1, exprs[h].end
	for ; j < len(exprs); j++ {
		e := exprs[j]
		if e.isHeader() && !hasPrefix(e.key, path) {
			break
		}
		if e.kind != exprComment {
			end = e.end
		}
	}
	for i, e := range exprs {
		if i >= h && i < j {
			continue
		}
		if e.kind != exprComment && !e.inArray && hasPrefix(e.key, path) {
			return 0, 0, fmt.Errorf("%w: parts of %q are written elsewhere in the file", ErrUnwritable, path)
		}
	}
	return exprs[h].start, end, nil
}

// tomlSet computes the splice that sets the table at path to value in body.
func tomlSet(body []byte, path []string, value any) (splice, int, error) {
	table, ok := value.(map[string]any)
	if !ok {
		return splice{}, 0, fmt.Errorf("%w: a TOML value must be a table, not %T", ErrInvalid, value)
	}
	doc, err := tomlDecode(body)
	if err != nil {
		return splice{}, 0, err
	}
	exprs, err := tomlScan(body)
	if err != nil {
		return splice{}, 0, err
	}
	if err := tomlCheckPath(exprs, path); err != nil {
		return splice{}, 0, err
	}
	existing := 0
	for i := 1; i <= len(path); i++ {
		v, ok := lookup(doc, path[:i])
		if !ok {
			break
		}
		if _, isTable := v.(map[string]any); !isTable && i < len(path) {
			return splice{}, 0, fmt.Errorf("%w: %q is not a table", ErrUnwritable, path[:i])
		}
		existing = i
	}
	nl := newline(body)
	text := renderTOMLTable(path, table, nl)

	var sp splice
	if existing == len(path) {
		start, end, err := tomlBlock(exprs, path)
		if err != nil {
			return splice{}, 0, err
		}
		old := body[start:end]
		if !bytes.HasSuffix(old, []byte("\n")) {
			text = strings.TrimSuffix(text, nl)
		}
		sp = splice{offset: start, old: old, new: []byte(text)}
	} else {
		// Append at the end: a [header] is valid anywhere, and nothing before it changes.
		switch {
		case len(body) == 0:
		case !bytes.HasSuffix(body, []byte("\n")):
			text = nl + nl + strings.TrimSuffix(text, nl)
		case endsWithBlankLine(body):
		default:
			text = nl + text
		}
		sp = splice{offset: len(body), new: []byte(text)}
	}
	// TOML has rules about where a table may be defined that the checks above do not cover
	// completely (dotted keys, for one). The parser has the last word.
	if err := toml.Unmarshal(sp.apply(body), &map[string]any{}); err != nil {
		return splice{}, 0, fmt.Errorf("%w: TOML does not allow [%s] to be added here: %v", ErrUnwritable, tomlKey(path), err)
	}
	return sp, len(path) - existing, nil
}

// endsWithBlankLine reports whether body, which ends with a line ending, is empty or ends with
// an empty line.
func endsWithBlankLine(body []byte) bool {
	b := bytes.TrimSuffix(bytes.TrimSuffix(body, []byte("\n")), []byte("\r"))
	return len(bytes.Trim(b, " \t\r\n")) == 0 || bytes.HasSuffix(b, []byte("\n"))
}

// tomlDelete removes the table at path, written as tomlBlock describes, and one empty line before
// it.
func tomlDelete(body []byte, path []string) ([]byte, error) {
	exprs, err := tomlScan(body)
	if err != nil {
		return nil, err
	}
	if err := tomlCheckPath(exprs, path); err != nil {
		return nil, err
	}
	start, end, err := tomlBlock(exprs, path)
	if err != nil {
		return nil, err
	}
	if k := start - 1; k >= 0 && body[k] == '\n' {
		if k > 0 && body[k-1] == '\r' {
			k--
		}
		if lineStart(body, k) == k {
			start = k
		}
	}
	return slices.Concat(body[:start], body[end:]), nil
}

// renderTOMLTable writes a table as "[path]" and its keys, then each sub-table the same way. A
// sub-table that holds nothing but sub-tables gets no header of its own.
func renderTOMLTable(path []string, table map[string]any, nl string) string {
	return renderTOMLTableAt(path, table, nl, true)
}

func renderTOMLTableAt(path []string, table map[string]any, nl string, top bool) string {
	var b strings.Builder
	var subs []string
	if top || len(table) == 0 || slices.ContainsFunc(sortedKeys(table), func(k string) bool {
		_, isTable := table[k].(map[string]any)
		return !isTable
	}) {
		b.WriteString("[" + tomlKey(path) + "]" + nl)
	}
	for _, k := range sortedKeys(table) {
		if _, ok := table[k].(map[string]any); ok {
			subs = append(subs, k)
			continue
		}
		b.WriteString(tomlKey([]string{k}) + " = " + tomlValue(table[k]) + nl)
	}
	for i, k := range subs {
		if i > 0 || b.Len() > 0 {
			b.WriteString(nl)
		}
		b.WriteString(renderTOMLTableAt(slices.Concat(path, []string{k}), table[k].(map[string]any), nl, false))
	}
	return b.String()
}

// tomlKey writes a dotted key, quoting the parts that are not bare keys.
func tomlKey(path []string) string {
	parts := make([]string, len(path))
	for i, p := range path {
		parts[i] = p
		for _, c := range []byte(p) {
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				parts[i] = tomlString(p)
				break
			}
		}
	}
	return strings.Join(parts, ".")
}

// tomlValue writes a value inline: arrays as [ … ], tables as { … }.
func tomlValue(v any) string {
	switch v := v.(type) {
	case string:
		return tomlString(v)
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		var s string
		if a := math.Abs(v); a != 0 && (a < 1e-6 || a >= 1e21) {
			s = strconv.FormatFloat(v, 'e', -1, 64)
		} else {
			s = strconv.FormatFloat(v, 'f', -1, 64)
		}
		if !strings.ContainsAny(s, ".e") {
			s += ".0" // without it, TOML reads an integer
		}
		return s
	case []any:
		parts := make([]string, len(v))
		for i, e := range v {
			parts[i] = tomlValue(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		if len(v) == 0 {
			return "{}"
		}
		parts := make([]string, 0, len(v))
		for _, k := range sortedKeys(v) {
			parts = append(parts, tomlKey([]string{k})+" = "+tomlValue(v[k]))
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	}
	panic(fmt.Sprintf("configedit: unchecked value of type %T", v))
}

// tomlString writes a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
