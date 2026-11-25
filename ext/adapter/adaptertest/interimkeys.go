// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adaptertest

// TODO(M0-20): delete this file, and its test, once internal/executor sets keys.
//
// internal/executor does not set keys in a tool's configuration file yet (it returns
// ErrNotImplemented); internal/configedit is being written as task M0-20. Until then the suite
// sets keys itself, with the least that lets every assertion mean something for a key: the
// checks the executor makes (prefix, a place the tool declared, unchanged since the plan, the
// file parses), an edit that inserts text and leaves every other byte alone, and an undo that
// takes exactly that text out again. Only JSON and JSONC, with the standard library; a TOML key
// is refused. It is not the product's editor and must not grow into one.
//
// coreExecutor (core.go) sends keys here only while the executor answers ErrNotImplemented.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
)

// interimKeys sets keys in configuration files inside one home directory.
type interimKeys struct {
	home   string
	places []string
}

// interimUndo is what interimKeys records to undo an entry.
type interimUndo struct {
	createdDirs []string // directories it created, outermost first
	insertion   insertion
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// resolve turns a path in the package form into a path on disk inside the home directory.
func (k *interimKeys) resolve(p string) (string, error) {
	var rel string
	switch {
	case strings.HasPrefix(p, "~/"):
		rel = p[2:]
	default:
		home := filepath.ToSlash(k.home)
		r, ok := strings.CutPrefix(path.Clean(p), strings.TrimSuffix(home, "/")+"/")
		if !ok {
			return "", fmt.Errorf("%w: %s is outside the home directory", errAdapterDefect, p)
		}
		rel = r
	}
	rel = path.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return "", fmt.Errorf("%w: %s leaves the home directory", errAdapterDefect, p)
	}
	return filepath.Join(k.home, filepath.FromSlash(rel)), nil
}

func checkJSONFormat(f adapter.Format) error {
	if f != adapter.JSON && f != adapter.JSONC {
		return fmt.Errorf("the suite sets %s keys only once internal/executor does (TODO(M0-20)); until then only JSON and JSONC", f)
	}
	return nil
}

func (k *interimKeys) expect(op adapter.SetKey) (string, error) {
	if err := checkJSONFormat(op.Format); err != nil {
		return "", err
	}
	c, err := canonical(op.Value)
	return hashString(c), err
}

func (k *interimKeys) observe(t target) observed {
	if err := checkJSONFormat(t.format); err != nil {
		return observed{state: unreadable, reason: err.Error()}
	}
	full, err := k.resolve(t.path)
	if err != nil {
		return observed{state: unreadable, reason: err.Error()}
	}
	data, err := os.ReadFile(full)
	if errors.Is(err, fs.ErrNotExist) {
		return observed{state: absent}
	}
	if err != nil {
		return observed{state: unreadable, reason: err.Error()}
	}
	doc, err := decodeJSON(data, t.format)
	if err != nil {
		return observed{state: unreadable, reason: t.path + " does not parse"}
	}
	v, ok := lookup(doc, t.key)
	if !ok {
		return observed{state: absent}
	}
	c, _ := canonical(v)
	return observed{state: present, digest: hashString(c)}
}

// mkdirs creates dir and its missing parents and returns those it created, outermost first.
func mkdirs(dir string) ([]string, error) {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append([]string{d}, missing...)
		if filepath.Dir(d) == d {
			break
		}
	}
	for _, d := range missing {
		if err := os.Mkdir(d, 0o755); err != nil {
			return nil, err
		}
	}
	return missing, nil
}

func (k *interimKeys) apply(op adapter.SetKey, pre observed) (entry, error) {
	t := targetOf(op)
	if err := checkOwned(op); err != nil {
		return entry{}, err
	}
	if err := checkJSONFormat(op.Format); err != nil {
		return entry{}, err
	}
	full, err := k.resolve(t.path)
	if err != nil {
		return entry{}, err
	}
	declared := false
	for _, p := range k.places {
		declared = declared || inside(path.Clean(t.path), p)
	}
	if !declared {
		return entry{}, fmt.Errorf("%w: %s lies outside the places the adapter declared", errAdapterDefect, t.path)
	}
	if now := k.observe(t); now != pre {
		return entry{}, errors.New("the target changed after the plan was made")
	}
	if pre.state == unreadable {
		return entry{}, fmt.Errorf("%s cannot be read, so it is not written: %s", t.path, pre.reason)
	}
	var undo interimUndo
	data, err := os.ReadFile(full)
	mode := fs.FileMode(0o644)
	switch {
	case err == nil:
		if info, err := os.Stat(full); err == nil {
			mode = info.Mode().Perm()
		}
	case errors.Is(err, fs.ErrNotExist):
		data = nil
	default:
		return entry{}, err
	}
	out, ins, err := insertJSON(data, op.Format, op.Path, op.Value)
	if err != nil {
		return entry{}, fmt.Errorf("%s: %w", t.path, err)
	}
	undo.insertion = ins
	if data == nil {
		if undo.createdDirs, err = mkdirs(filepath.Dir(full)); err != nil {
			return entry{}, err
		}
	}
	if err := os.WriteFile(full, out, mode); err != nil {
		return entry{}, err
	}
	after := k.observe(t)
	want, err := k.expect(op)
	if err != nil {
		return entry{}, err
	}
	if after.state != present || after.digest != want {
		return entry{}, fmt.Errorf("%s: what was written does not read back as planned", t)
	}
	return entry{target: t, ref: op.Ref(), digest: after.digest, undo: undo}, nil
}

func (k *interimKeys) revert(en entry, undo interimUndo) (bool, error) {
	full, err := k.resolve(en.target.path)
	if err != nil {
		return false, err
	}
	now := k.observe(en.target)
	data, err := os.ReadFile(full)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return false, err
	case now.state == unreadable:
		return false, fmt.Errorf("%s cannot be read: %s", en.target, now.reason)
	case now.state == present && now.digest != en.digest:
		return false, errors.New("the suite cannot remove a key whose value was changed by hand (TODO(M0-20))")
	case now.state == present:
		out, remove, err := undoInsertion(data, undo.insertion)
		if err != nil {
			return false, fmt.Errorf("%s: %w (the real executor falls back to removing the key by its meaning)", en.target, err)
		}
		if remove {
			err = os.Remove(full)
		} else {
			err = os.WriteFile(full, out, 0o644)
		}
		if err != nil {
			return false, err
		}
	}
	for i := len(undo.createdDirs) - 1; i >= 0; i-- {
		entries, err := os.ReadDir(undo.createdDirs[i])
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(undo.createdDirs[i]); err != nil {
			return false, err
		}
	}
	return false, nil
}

// insertion is how to undo a key we added: the text inserted at an offset, or the whole file
// when we created it.
type insertion struct {
	Offset  int
	Text    string
	Created bool // the file did not exist; Text is its whole content
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
