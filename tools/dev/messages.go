// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The message catalogues live in locales/<tag>/<domain>.json: flat JSON
// objects of key -> text in the i18next v4 conventions. This file reads them
// for two tasks: gen (sort every file, generate the Go constants for the
// keys) and the "messages" step of check (the rules of docs 09-i18n).

const localesDir = "locales"

// sourceLanguage is the language every other one falls back to, and the one
// the Go constants are generated from.
const sourceLanguage = "en"

// requiredLanguages must be complete: the same keys and placeholders as the
// source. Other languages may be incomplete; what they lack falls back to
// English.
var requiredLanguages = []string{"en", "zh-CN"}

// metaFile is a language's own name and maintainers, not messages.
const metaFile = "meta.json"

// webDomains are the files only the web console uses. Their keys are used
// from TypeScript, which the usage rule cannot see yet.
var webDomains = []string{"web"}

// msgPackage is where the constants for the keys are generated.
const msgPackage = "internal/i18n/msg"

// pluralForms are the CLDR plural categories as key suffixes:
// sync.plan.total_one, sync.plan.total_other.
var pluralForms = []string{"zero", "one", "two", "few", "many", "other"}

// placeholderPattern matches {{name}}; internal/i18n uses the same pattern.
var placeholderPattern = regexp.MustCompile(`\{\{[A-Za-z0-9_]+\}\}`)

// languageTag is the shape of a directory name under locales/: a BCP 47 tag
// such as en, zh-CN or sr-Latn.
var languageTag = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// messageFile is one locales/<lang>/<domain>.json.
type messageFile struct {
	lang, domain string
	rel          string   // path relative to the repository root, with slashes
	keys         []string // in file order
	msgs         map[string]string
	problems     []string // duplicates, wrong order, values that are not text
}

// locales is every message file, by language and domain.
type locales struct {
	langs []string                              // sorted
	files map[string]map[string]*messageFile    // lang -> domain -> file
	metas map[string]map[string]json.RawMessage // lang -> meta.json, nil when missing
	other map[string][]string                   // lang -> files that are neither messages nor meta
}

// readLocales reads locales/. It returns (nil, nil) when the directory does
// not exist. A file that is not JSON at all is an error; smaller problems are
// recorded on the file.
func readLocales(root string) (*locales, error) {
	dir := filepath.Join(root, localesDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	l := &locales{
		files: map[string]map[string]*messageFile{},
		metas: map[string]map[string]json.RawMessage{},
		other: map[string][]string{},
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		lang := e.Name()
		l.langs = append(l.langs, lang)
		l.files[lang] = map[string]*messageFile{}
		files, err := os.ReadDir(filepath.Join(dir, lang))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			rel := path.Join(localesDir, lang, f.Name())
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				l.other[lang] = append(l.other[lang], rel)
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, lang, f.Name()))
			if err != nil {
				return nil, err
			}
			if f.Name() == metaFile {
				var meta map[string]json.RawMessage
				if err := json.Unmarshal(data, &meta); err != nil {
					return nil, fmt.Errorf("%s: %w", rel, err)
				}
				l.metas[lang] = meta
				continue
			}
			mf, err := parseMessageFile(data)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", rel, err)
			}
			mf.lang, mf.domain, mf.rel = lang, strings.TrimSuffix(f.Name(), ".json"), rel
			l.files[lang][mf.domain] = mf
		}
	}
	sort.Strings(l.langs)
	return l, nil
}

// parseMessageFile reads a flat JSON object token by token, so that
// duplicate keys and the order of keys can be seen (encoding/json would
// silently keep the last duplicate).
func parseMessageFile(data []byte) (*messageFile, error) {
	mf := &messageFile{msgs: map[string]string{}}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object of key -> text")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key := tok.(string) // object keys are always strings
		var value any
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		text, ok := value.(string)
		switch {
		case !ok:
			mf.problems = append(mf.problems, fmt.Sprintf("the value of %q is not text: keys are flat, values are strings", key))
			continue
		case slices.Contains(mf.keys, key):
			mf.problems = append(mf.problems, fmt.Sprintf("key %q appears twice", key))
			continue
		}
		if n := len(mf.keys); n > 0 && mf.keys[n-1] > key {
			mf.problems = append(mf.problems, fmt.Sprintf("key %q is out of order (keys are sorted; go run ./tools/dev gen sorts them)", key))
		}
		mf.keys = append(mf.keys, key)
		mf.msgs[key] = text
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("more than one JSON value")
	}
	return mf, nil
}

// canonical returns the file as gen writes it: keys sorted, two-space
// indentation, no HTML escaping, a final newline.
func (mf *messageFile) canonical() ([]byte, error) {
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range mf.msgs {
			if !yield(k) {
				return
			}
		}
	})
	var b bytes.Buffer
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n  ")
		b.Write(jsonString(k))
		b.WriteString(": ")
		b.Write(jsonString(mf.msgs[k]))
	}
	if len(keys) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.Bytes(), nil
}

func jsonString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s) // a string always encodes
	return bytes.TrimRight(b.Bytes(), "\n")
}

// baseKey strips a plural suffix: sync.plan.total_one -> sync.plan.total.
func baseKey(key string) (base, form string) {
	for _, f := range pluralForms {
		if b, ok := strings.CutSuffix(key, "_"+f); ok {
			return b, f
		}
	}
	return key, ""
}

// initialisms are written in capitals in the constant names, as Go does.
var initialisms = map[string]bool{
	"api": true, "ca": true, "cli": true, "html": true, "http": true, "https": true,
	"id": true, "json": true, "mcp": true, "sql": true, "ui": true, "uri": true, "url": true,
}

// constName turns a key into the name of its Go constant:
// config.reason.url -> ConfigReasonURL.
func constName(key string) string {
	var b strings.Builder
	for _, part := range strings.FieldsFunc(key, func(r rune) bool { return r == '.' || r == '_' || r == '-' }) {
		if initialisms[part] {
			b.WriteString(strings.ToUpper(part))
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}

// goDomain reports whether Go code uses the domain's messages.
func goDomain(domain string) bool { return !slices.Contains(webDomains, domain) }

// messageKeys are the keys the Go code refers to, by their constant names.
type messageKey struct {
	key, constName, domain, text string
}

// goKeys returns the base keys of the source language's Go domains, sorted.
// Two keys that would get one constant name are an error.
func (l *locales) goKeys() ([]messageKey, error) {
	seen := map[string]messageKey{}
	byName := map[string]string{}
	for domain, mf := range l.files[sourceLanguage] {
		if !goDomain(domain) {
			continue
		}
		for _, k := range mf.keys {
			base, form := baseKey(k)
			if _, dup := seen[base]; dup {
				continue
			}
			text := mf.msgs[k]
			if form != "" {
				text = mf.msgs[base+"_other"]
			}
			name := constName(base)
			if other, clash := byName[name]; clash && other != base {
				return nil, fmt.Errorf("keys %q and %q would both become the constant %s; rename one", other, base, name)
			}
			byName[name] = base
			seen[base] = messageKey{key: base, constName: name, domain: domain, text: text}
		}
	}
	keys := make([]messageKey, 0, len(seen))
	for _, k := range seen {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].key < keys[j].key })
	return keys, nil
}

// generateMessages returns the generated files that come from locales/: the
// sorted message files and the Go constants. Nothing when there is no
// locales/ directory.
func generateMessages(root string) ([]generated, error) {
	l, err := readLocales(root)
	if err != nil || l == nil {
		return nil, err
	}
	var out []generated
	for _, lang := range l.langs {
		for _, mf := range l.files[lang] {
			data, err := mf.canonical()
			if err != nil {
				return nil, err
			}
			out = append(out, generated{path: mf.rel, data: data})
		}
	}
	if _, ok := l.files[sourceLanguage]; !ok {
		return out, nil
	}
	keys, err := l.goKeys()
	if err != nil {
		return nil, err
	}
	src, err := keysGo(keys)
	if err != nil {
		return nil, err
	}
	out = append(out, generated{path: msgPackage + "/keys_gen.go", data: src})
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

func keysGo(keys []messageKey) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("// Code generated by \"go run ./tools/dev gen\" from " + localesDir + "/" + sourceLanguage + ". DO NOT EDIT.\n\n")
	b.WriteString("package msg\n\nconst (\n")
	for _, k := range keys {
		text, _, _ := strings.Cut(k.text, "\n")
		if r := []rune(text); len(r) > 70 {
			text = string(r[:70]) + "…"
		}
		fmt.Fprintf(&b, "\t// %s (%s.json): %s\n\t%s = %q\n", k.constName, k.domain, text, k.constName, k.key)
	}
	b.WriteString(")\n")
	return format.Source(b.Bytes())
}

// checkMessages applies the rules for the catalogues and returns a summary
// for the result table, or every problem found.
func checkMessages(root, module string, goFiles []goFile) (string, error) {
	l, err := readLocales(root)
	if err != nil {
		return "", err
	}
	if l == nil {
		return "", errNotRun{"there is no locales/ directory yet"}
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	for _, lang := range l.langs {
		if !languageTag.MatchString(lang) {
			add("%s/%s: the directory name is not a language tag such as en or zh-CN", localesDir, lang)
		}
		if meta := l.metas[lang]; meta == nil {
			add("%s/%s/%s is missing: it holds the language's own name", localesDir, lang, metaFile)
		} else if name, ok := meta["name"]; !ok || string(name) == `""` {
			add("%s/%s/%s: \"name\" (the language's name in itself) is missing", localesDir, lang, metaFile)
		}
		for _, rel := range l.other[lang] {
			add("%s: only <domain>.json and %s belong in a language directory", rel, metaFile)
		}
		domains := slices.Sorted(func(yield func(string) bool) {
			for d := range l.files[lang] {
				if !yield(d) {
					return
				}
			}
		})
		owner := map[string]string{}
		for _, d := range domains {
			mf := l.files[lang][d]
			for _, p := range mf.problems {
				add("%s: %s", mf.rel, p)
			}
			for _, k := range mf.keys {
				if other, dup := owner[k]; dup {
					add("%s: key %q is also in %s; a key lives in one file", mf.rel, k, other)
				}
				owner[k] = mf.rel
				if base, form := baseKey(k); form != "" {
					if _, plain := mf.msgs[base]; plain {
						add("%s: %q has plural forms and a plain form; keep only the forms", mf.rel, base)
					}
					if _, ok := mf.msgs[base+"_other"]; !ok {
						add("%s: %q has plural forms but no %s_other, which every language needs", mf.rel, base, base)
					}
				}
			}
		}
	}
	for _, lang := range requiredLanguages {
		if !slices.Contains(l.langs, lang) {
			add("%s/%s is missing: %s is a required language", localesDir, lang, lang)
		}
	}
	src := l.files[sourceLanguage]
	if src == nil {
		return "", errors.New(strings.Join(problems, "\n"))
	}

	// Keys and placeholders against the source language.
	completion := map[string][2]int{} // lang -> translated, total
	for _, lang := range l.langs {
		if lang == sourceLanguage {
			continue
		}
		required := slices.Contains(requiredLanguages, lang)
		var done, total int
		for _, domain := range slices.Sorted(mapKeys(src)) {
			sf, tf := src[domain], l.files[lang][domain]
			for _, k := range sf.keys {
				total++
				if tf == nil {
					if required {
						add("%s/%s/%s.json: missing key %q (en: %q)", localesDir, lang, domain, k, sf.msgs[k])
					}
					continue
				}
				if _, ok := tf.msgs[k]; ok {
					done++
				} else if required {
					add("%s: missing key %q (en: %q)", tf.rel, k, sf.msgs[k])
				}
			}
		}
		for domain, tf := range l.files[lang] {
			sf := src[domain]
			for _, k := range tf.keys {
				// A language that is not required may have plural
				// categories English lacks (such as _few); they are
				// compared with English's _other.
				enKey := k
				if sf != nil && !required {
					if _, ok := sf.msgs[k]; !ok {
						if base, form := baseKey(k); form != "" {
							enKey = base + "_other"
						}
					}
				}
				enText, ok := "", false
				if sf != nil {
					enText, ok = sf.msgs[enKey]
				}
				if !ok {
					add("%s: key %q is not in %s; remove it", tf.rel, k, path.Join(localesDir, sourceLanguage, domain+".json"))
					continue
				}
				_, form := baseKey(k)
				if want, got := placeholders(enText, form != ""), placeholders(tf.msgs[k], form != ""); !slices.Equal(want, got) {
					add("%s: key %q has the placeholders %s, but en has %s", tf.rel, k, braces(got), braces(want))
				}
			}
		}
		completion[lang] = [2]int{done, total}
	}

	// Every key Go code can use is used, and every literal key passed to T
	// exists.
	used, unknown := usedKeys(module, goFiles, l)
	keys, err := l.goKeys()
	if err != nil {
		add("%v", err)
	}
	for _, k := range keys {
		if !used[k.key] {
			add("%s/%s/%s.json: key %q (constant msg.%s) is not used by any code; remove it from every language",
				localesDir, sourceLanguage, k.domain, k.key, k.constName)
		}
	}
	problems = append(problems, unknown...)

	if len(problems) > 0 {
		return "", fmt.Errorf("%d problem(s) in %s/:\n  %s", len(problems), localesDir, strings.Join(problems, "\n  "))
	}
	var optional []string
	for _, lang := range l.langs {
		if c, ok := completion[lang]; ok && !slices.Contains(requiredLanguages, lang) {
			pct := 100
			if c[1] > 0 {
				pct = c[0] * 100 / c[1]
			}
			optional = append(optional, fmt.Sprintf("%s %d%%", lang, pct))
		}
	}
	other := "none"
	if len(optional) > 0 {
		other = strings.Join(optional, ", ")
	}
	detail := fmt.Sprintf("%s complete (%d keys), placeholders match, every key used; other languages: %s",
		strings.Join(requiredLanguages, ", "), len(keys), other)
	for _, d := range webDomains {
		if src[d] != nil {
			detail += "; " + d + ".json usage not checked (TypeScript)"
		}
	}
	return detail, nil
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// placeholders returns the sorted, distinct placeholders of a text. In a
// plural form {{count}} is left out: "one file" need not repeat the number.
func placeholders(text string, plural bool) []string {
	var out []string
	for _, m := range placeholderPattern.FindAllString(text, -1) {
		name := m[2 : len(m)-2]
		if plural && name == "count" {
			continue
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func braces(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "{{" + n + "}}"
	}
	return strings.Join(out, " ")
}

// usedKeys finds which message keys the Go code uses: through a constant of
// the generated msg package, or spelled out as a string literal (built-in
// implementations cannot import internal/ and return their message keys as
// strings). It also reports calls of T with a literal key that does not
// exist. Test files and generated files do not count.
func usedKeys(module string, goFiles []goFile, l *locales) (map[string]bool, []string) {
	all := map[string]bool{} // every key and base key of the source language
	byConst := map[string]string{}
	for _, mf := range l.files[sourceLanguage] {
		for _, k := range mf.keys {
			base, _ := baseKey(k)
			all[k], all[base] = true, true
			byConst[constName(base)] = base
		}
	}
	used := map[string]bool{}
	var unknown []string
	for _, f := range goFiles {
		if f.test {
			continue
		}
		msgNames := f.importNames(func(p string) bool { return p == module+"/"+msgPackage })
		usesI18n := len(f.importNames(func(p string) bool { return p == module+"/internal/i18n" })) > 0
		ast.Inspect(f.ast, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok && slices.Contains(msgNames, id.Name) {
					if key, ok := byConst[x.Sel.Name]; ok {
						used[key] = true
					}
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					if s, err := strconv.Unquote(x.Value); err == nil && all[s] {
						base, _ := baseKey(s)
						used[base] = true
					}
				}
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !usesI18n || !ok || sel.Sel.Name != "T" || len(x.Args) == 0 {
					return true
				}
				if lit, ok := x.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil && !all[s] {
						unknown = append(unknown, fmt.Sprintf("%s: message key %q does not exist in %s/%s", f.position(lit), s, localesDir, sourceLanguage))
					}
				}
			}
			return true
		})
	}
	return used, unknown
}

// moduleOf reads the module path from go.mod.
func moduleOf(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), nil
		}
	}
	return "", errors.New("go.mod has no module line")
}

// stepMessages is the "messages" step of check.
func stepMessages(s *sandbox) (string, error) {
	module, err := moduleOf(s.root)
	if err != nil {
		return "", err
	}
	files, err := walkSource(s.root)
	if err != nil {
		return "", err
	}
	goFiles, err := parseGo(files)
	if err != nil {
		return "", err
	}
	return checkMessages(s.root, module, goFiles)
}
