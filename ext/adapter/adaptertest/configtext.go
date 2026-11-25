// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adaptertest

// This file edits configuration files as text, for the stand-in executor (standin.go): a key is
// added by inserting text and removed by taking exactly that text out again, so that every other
// byte stays as it was. It is deliberately small. The real editor is internal/configedit, which
// handles far more (files rewritten by the tool in between, keys inside inline TOML tables, …).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
)

// errUnparsable: the file does not parse, so nothing may be written to it.
var errUnparsable = errors.New("the file does not parse")

// insertion is how to undo a key we added: the text inserted at an offset, or the whole file
// when we created it.
type insertion struct {
	Offset  int
	Text    string
	Created bool // the file did not exist; Text is its whole content
}

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

// jsonObject is an object in the text of a JSON file: where its braces are and where its members
// end.
type jsonObject struct {
	open, close int
	members     []jsonMember
}

type jsonMember struct {
	key        string
	keyStart   int
	valueEnd   int
	valueIsObj *jsonObject
}

// scanJSON finds the objects of a file that StandardJSON has made strict. It assumes the text is
// valid JSON.
type scanJSON struct {
	s   []byte
	pos int
}

func (p *scanJSON) space() {
	for p.pos < len(p.s) && strings.IndexByte(" \t\r\n", p.s[p.pos]) >= 0 {
		p.pos++
	}
}

// value skips one value and returns it as an object when it is one.
func (p *scanJSON) value() *jsonObject {
	p.space()
	switch p.s[p.pos] {
	case '{':
		o := &jsonObject{open: p.pos}
		p.pos++
		for {
			p.space()
			if p.s[p.pos] == '}' {
				o.close = p.pos
				p.pos++
				return o
			}
			if p.s[p.pos] == ',' {
				p.pos++
				continue
			}
			keyStart := p.pos
			p.str()
			var key string
			_ = json.Unmarshal(p.s[keyStart:p.pos], &key)
			p.space()
			p.pos++ // ':'
			child := p.value()
			o.members = append(o.members, jsonMember{key: key, keyStart: keyStart, valueEnd: p.pos, valueIsObj: child})
		}
	case '[':
		p.pos++
		for {
			p.space()
			if p.s[p.pos] == ']' {
				p.pos++
				return nil
			}
			if p.s[p.pos] == ',' {
				p.pos++
				continue
			}
			p.value()
		}
	case '"':
		p.str()
	default:
		for p.pos < len(p.s) && strings.IndexByte(",}] \t\r\n", p.s[p.pos]) < 0 {
			p.pos++
		}
	}
	return nil
}

func (p *scanJSON) str() {
	p.pos++
	for p.s[p.pos] != '"' {
		if p.s[p.pos] == '\\' {
			p.pos++
		}
		p.pos++
	}
	p.pos++
}

// lineIndent returns the white space at the start of the line that holds offset, and whether
// nothing but white space comes before offset on that line.
func lineIndent(s []byte, offset int) (string, bool) {
	start := bytes.LastIndexByte(s[:offset], '\n') + 1
	lead := s[start:offset]
	trimmed := bytes.TrimLeft(lead, " \t")
	return string(lead[:len(lead)-len(trimmed)]), len(trimmed) == 0
}

// insertJSON adds the key at path to a JSON or JSONC file (nil data: the file does not exist).
// The key must not exist yet. Levels of path that do not exist are created.
func insertJSON(data []byte, format adapter.Format, path []string, value any) ([]byte, insertion, error) {
	if data == nil {
		var nested any = value
		for i := len(path) - 1; i >= 0; i-- {
			nested = map[string]any{path[i]: nested}
		}
		b, err := marshalIndent(nested, "", "  ")
		if err != nil {
			return nil, insertion{}, err
		}
		text := string(b) + "\n"
		return []byte(text), insertion{Text: text, Created: true}, nil
	}
	doc, err := decodeJSON(data, format)
	if err != nil {
		return nil, insertion{}, err
	}
	if _, exists := lookup(doc, path); exists {
		return nil, insertion{}, fmt.Errorf("the key %v exists already", path)
	}
	clean := adapter.StandardJSON(data, format == adapter.JSONC)
	scan := &scanJSON{s: clean}
	obj := scan.value()
	if obj == nil {
		return nil, insertion{}, errors.New("the top level is not an object")
	}
	// One level of indentation, as the file has it: how far the first member of the top level
	// is indented.
	unit := "  "
	if len(obj.members) > 0 {
		if ind, alone := lineIndent(data, obj.members[0].keyStart); alone && ind != "" {
			unit = ind
		}
	}
	depth := 0
	for ; depth < len(path)-1; depth++ {
		var next *jsonObject
		found := false
		for _, m := range obj.members {
			if m.key == path[depth] {
				found, next = true, m.valueIsObj
			}
		}
		if !found {
			break
		}
		if next == nil {
			return nil, insertion{}, fmt.Errorf("%s is not an object", strings.Join(path[:depth+1], "."))
		}
		obj = next
	}
	var nested any = value
	for i := len(path) - 1; i > depth; i-- {
		nested = map[string]any{path[i]: nested}
	}

	nl := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		nl = "\r\n"
	}
	braceIndent, _ := lineIndent(data, obj.open)
	var indent string
	compact := false
	if len(obj.members) > 0 {
		var alone bool
		indent, alone = lineIndent(data, obj.members[0].keyStart)
		compact = !alone || bytes.LastIndexByte(data[:obj.members[0].keyStart], '\n') < obj.open
	} else {
		indent = braceIndent + unit
	}

	key, _ := json.Marshal(path[depth])
	var member string
	if compact {
		v, err := marshal(nested)
		if err != nil {
			return nil, insertion{}, err
		}
		member = string(key) + ": " + string(v)
	} else {
		v, err := marshalIndent(nested, indent, unit)
		if err != nil {
			return nil, insertion{}, err
		}
		member = string(key) + ": " + strings.ReplaceAll(string(v), "\n", nl)
	}
	var text string
	var at int
	switch {
	case len(obj.members) > 0 && compact:
		at, text = obj.members[len(obj.members)-1].valueEnd, ", "+member
	case len(obj.members) > 0:
		at, text = obj.members[len(obj.members)-1].valueEnd, ","+nl+indent+member
	default:
		at, text = obj.open+1, nl+indent+member+nl+braceIndent
	}
	out := make([]byte, 0, len(data)+len(text))
	out = append(append(append(out, data[:at]...), text...), data[at:]...)

	// What changed has to be that one key, and nothing else.
	after, err := decodeJSON(out, format)
	if err != nil {
		return nil, insertion{}, fmt.Errorf("the edited file does not parse: %w", err)
	}
	if !reflect.DeepEqual(without(after, path[:depth+1]), doc) {
		return nil, insertion{}, errors.New("the edit changed more than the one key")
	}
	return out, insertion{Offset: at, Text: text}, nil
}

func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

func marshalIndent(v any, prefix, indent string) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent(prefix, indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// undoInsertion takes an insertion out again. It fails when the inserted text is no longer where
// it was put, because then the file has been rewritten and the bytes cannot be restored.
func undoInsertion(data []byte, ins insertion) ([]byte, bool, error) {
	if ins.Created {
		if string(data) == ins.Text {
			return nil, true, nil // remove the file
		}
		return nil, false, errors.New("the file we created has been changed since")
	}
	end := ins.Offset + len(ins.Text)
	if end > len(data) || string(data[ins.Offset:end]) != ins.Text {
		return nil, false, errors.New("the file has been rewritten since the key was added")
	}
	out := make([]byte, 0, len(data)-len(ins.Text))
	return append(append(out, data[:ins.Offset]...), data[end:]...), false, nil
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func tomlKey(path []string) string {
	parts := make([]string, len(path))
	for i, k := range path {
		if bareKey.MatchString(k) {
			parts[i] = k
		} else {
			q, _ := json.Marshal(k)
			parts[i] = string(q)
		}
	}
	return strings.Join(parts, ".")
}

// tomlBlock writes value as a TOML table at path: scalars and lists first, then one sub-table
// for each map. Lines end in "\n".
func tomlBlock(path []string, value any) (string, error) {
	m, ok := value.(map[string]any)
	if !ok {
		return "", fmt.Errorf("a TOML table needs a map, not %T", value)
	}
	var b strings.Builder
	b.WriteString("[" + tomlKey(path) + "]\n")
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var tables []string
	for _, k := range keys {
		if _, isMap := m[k].(map[string]any); isMap {
			tables = append(tables, k)
			continue
		}
		v, err := tomlValue(m[k])
		if err != nil {
			return "", fmt.Errorf("%s: %w", k, err)
		}
		b.WriteString(tomlKey([]string{k}) + " = " + v + "\n")
	}
	for _, k := range tables {
		sub, err := tomlBlock(append(append([]string{}, path...), k), m[k])
		if err != nil {
			return "", err
		}
		b.WriteString(sub)
	}
	return b.String(), nil
}

func tomlValue(v any) (string, error) {
	switch v := v.(type) {
	case string:
		q, err := marshal(v)
		return string(q), err
	case bool, int, int64, float64:
		return fmt.Sprint(v), nil
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			s, err := tomlValue(item)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	}
	return "", fmt.Errorf("cannot write %T in TOML", v)
}

// appendTOML adds a table at the end of a TOML file (nil data: the file does not exist). The
// stand-in cannot parse TOML; the real executor checks the result with a parser.
func appendTOML(data []byte, path []string, value any) ([]byte, insertion, error) {
	block, err := tomlBlock(path, value)
	if err != nil {
		return nil, insertion{}, err
	}
	if data == nil {
		return []byte(block), insertion{Text: block, Created: true}, nil
	}
	if _, found := findTOMLBlock(data, path); found {
		return nil, insertion{}, fmt.Errorf("the table %s exists already", tomlKey(path))
	}
	nl := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		nl = "\r\n"
	}
	text := nl + strings.ReplaceAll(block, "\n", nl)
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		text = nl + text
	}
	return append(bytes.Clone(data), text...), insertion{Offset: len(data), Text: text}, nil
}

// findTOMLBlock returns the text of the table at path and its sub-tables, with "\n" line ends
// and without trailing blank lines.
func findTOMLBlock(data []byte, path []string) (string, bool) {
	header := "[" + tomlKey(path) + "]"
	subPrefix := "[" + tomlKey(path) + "."
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == header {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "[") && !strings.HasPrefix(l, subPrefix) {
			end = i
			break
		}
	}
	block := lines[start:end]
	for len(block) > 0 && strings.TrimSpace(block[len(block)-1]) == "" {
		block = block[:len(block)-1]
	}
	return strings.Join(block, "\n") + "\n", true
}
