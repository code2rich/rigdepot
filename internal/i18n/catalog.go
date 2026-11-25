// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package i18n

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"

	"github.com/code2rich/rigdepot/internal/brand"
)

// Fallback is the language every other language falls back to. A key missing
// here is a defect, and the message check in tools/dev stops it.
const Fallback = "en"

// metaFile holds a language's own name; it is not a message file.
const metaFile = "meta.json"

// Language is one language of a catalog.
type Language struct {
	Tag  language.Tag
	Name string // the language's name in itself, such as "简体中文", from meta.json
}

// Catalog holds the messages of every language found in a directory tree laid
// out as <tag>/<domain>.json, such as en/cli.json. Which languages exist is
// read from the directory names, so adding a language needs no code.
type Catalog struct {
	langs   []Language
	msgs    map[language.Tag]map[string]string
	matcher language.Matcher
}

// Load reads the given domains ("catalog", "check", "cli", ...) of every
// language in fsys. A language without one of the files simply lacks those
// messages and falls back to English; a file that exists but cannot be read
// or parsed is an error, never an empty set. The fallback language must
// exist.
func Load(fsys fs.FS, domains ...string) (*Catalog, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("i18n: %w", err)
	}
	c := &Catalog{msgs: map[language.Tag]map[string]string{}}
	fallback := language.MustParse(Fallback)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		tag, err := language.Parse(e.Name())
		if err != nil || tag.String() != e.Name() {
			return nil, fmt.Errorf("i18n: directory %q is not a canonical BCP 47 language tag", e.Name())
		}
		msgs := map[string]string{}
		for _, domain := range domains {
			file := path.Join(e.Name(), domain+".json")
			data, err := fs.ReadFile(fsys, file)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("i18n: %w", err)
			}
			if err := decodeMessages(data, msgs); err != nil {
				return nil, fmt.Errorf("i18n: %s: %w", file, err)
			}
		}
		lang := Language{Tag: tag, Name: e.Name()}
		if data, err := fs.ReadFile(fsys, path.Join(e.Name(), metaFile)); err == nil {
			var meta struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(data, &meta); err != nil {
				return nil, fmt.Errorf("i18n: %s/%s: %w", e.Name(), metaFile, err)
			}
			if meta.Name != "" {
				lang.Name = meta.Name
			}
		}
		c.msgs[tag] = msgs
		c.langs = append(c.langs, lang)
	}
	if _, ok := c.msgs[fallback]; !ok {
		return nil, fmt.Errorf("i18n: the fallback language %q is missing", Fallback)
	}
	// The fallback goes first: the matcher returns the first tag when
	// nothing matches.
	sort.SliceStable(c.langs, func(i, j int) bool {
		if c.langs[i].Tag == fallback || c.langs[j].Tag == fallback {
			return c.langs[i].Tag == fallback
		}
		return c.langs[i].Tag.String() < c.langs[j].Tag.String()
	})
	tags := make([]language.Tag, len(c.langs))
	for i, l := range c.langs {
		tags[i] = l.Tag
	}
	c.matcher = language.NewMatcher(tags)
	return c, nil
}

// decodeMessages adds the flat key -> text object in data to msgs. A key that
// is already there (from another domain file) is an error: keys start with
// the feature they belong to and are unique across files.
func decodeMessages(data []byte, msgs map[string]string) error {
	var raw map[string]string
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("more than one JSON value")
	}
	for k, v := range raw {
		if _, dup := msgs[k]; dup {
			return fmt.Errorf("key %q is defined in more than one file", k)
		}
		msgs[k] = v
	}
	return nil
}

// Languages returns the languages of the catalog, the fallback first.
func (c *Catalog) Languages() []Language {
	return append([]Language(nil), c.langs...)
}

// Match picks the catalog language for a list of preferences, most preferred
// first, such as the values of --lang, the environment and the system
// language. Preferences that are not language tags are skipped. Chinese in
// any script or region falls back to the Chinese the catalog has rather than
// to English: until there is a zh-TW, Traditional Chinese readers get zh-CN.
func (c *Catalog) Match(prefs ...string) language.Tag {
	for _, pref := range prefs {
		tag, err := language.Parse(strings.ReplaceAll(strings.TrimSpace(pref), "_", "-"))
		if err != nil || tag == language.Und {
			continue
		}
		_, index, conf := c.matcher.Match(tag)
		if conf != language.No {
			return c.langs[index].Tag
		}
		if t, ok := c.sameBase(tag); ok {
			return t
		}
	}
	return c.langs[0].Tag
}

// sameBase returns a catalog language with the same base language as tag,
// preferring Simplified Chinese for Chinese. The matcher on its own treats
// the two Chinese scripts as too different to match.
func (c *Catalog) sameBase(tag language.Tag) (language.Tag, bool) {
	base, _ := tag.Base()
	var found []language.Tag
	for _, l := range c.langs {
		if b, _ := l.Tag.Base(); b == base {
			found = append(found, l.Tag)
		}
	}
	if len(found) == 0 {
		return language.Und, false
	}
	for _, t := range found {
		if t.String() == "zh-CN" {
			return t, true
		}
	}
	return found[0], true
}

// Printer renders messages in one language.
type Printer struct {
	tag      language.Tag
	fallback language.Tag
	cat      *Catalog
}

// Printer returns a printer for a language of the catalog; any other tag is
// matched first.
func (c *Catalog) Printer(tag language.Tag) *Printer {
	if _, ok := c.msgs[tag]; !ok {
		tag = c.Match(tag.String())
	}
	return &Printer{tag: tag, fallback: language.MustParse(Fallback), cat: c}
}

// Lang returns the language the printer renders.
func (p *Printer) Lang() language.Tag { return p.tag }

// Arg is a value for a placeholder of a message.
type Arg struct {
	name  string
	value string
	count *int
}

// V fills the placeholder {{name}}. The value is used as it is: numbers and
// dates are formatted before they get here.
func V(name string, value any) Arg {
	return Arg{name: name, value: fmt.Sprint(value)}
}

// Count fills {{count}} and picks the plural form of the message by n.
func Count(n int) Arg {
	return Arg{name: "count", value: strconv.Itoa(n), count: &n}
}

// Placeholders that every message can use without passing them: the names
// come from brand.json, so that a rename does not touch the translations.
var brandArgs = map[string]string{
	"cli":     brand.CLI,
	"product": brand.Name,
	"server":  brand.Server,
	"home":    brand.Home,
}

// pluralSuffixes are the CLDR plural categories as they appear at the end of
// a key, as in sync.plan.total_one.
var pluralSuffixes = map[plural.Form]string{
	plural.Other: "_other", plural.Zero: "_zero", plural.One: "_one",
	plural.Two: "_two", plural.Few: "_few", plural.Many: "_many",
}

// T renders the message key with args. With Count, the plural form for the
// count is looked up (key_one, key_other, ...). A message missing in the
// printer's language comes from the fallback language; missing there too, T
// returns the key itself so that the defect is visible.
func (p *Printer) T(key string, args ...Arg) string {
	var count *int
	values := make(map[string]string, len(args))
	for _, a := range args {
		values[a.name] = a.value
		if a.count != nil {
			count = a.count
		}
	}
	text, ok := p.lookup(p.tag, key, count)
	if !ok {
		text, ok = p.lookup(p.fallback, key, count)
	}
	if !ok {
		return key
	}
	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		name := m[2 : len(m)-2]
		if v, ok := values[name]; ok {
			return v
		}
		if v, ok := brandArgs[name]; ok {
			return v
		}
		return m
	})
}

func (p *Printer) lookup(tag language.Tag, key string, count *int) (string, bool) {
	if p.cat == nil {
		return "", false
	}
	msgs := p.cat.msgs[tag]
	if count != nil {
		form := plural.Cardinal.MatchPlural(tag, abs(*count), 0, 0, 0, 0)
		if text, ok := msgs[key+pluralSuffixes[form]]; ok {
			return text, true
		}
		if text, ok := msgs[key+pluralSuffixes[plural.Other]]; ok {
			return text, true
		}
	}
	text, ok := msgs[key]
	return text, ok
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// placeholder matches {{name}}. The message check in tools/dev uses the same
// pattern.
var placeholder = regexp.MustCompile(`\{\{[A-Za-z0-9_]+\}\}`)

// Parse reads a language tag as a person types it: en, zh-CN, or the POSIX
// form zh_CN.
func Parse(s string) (language.Tag, error) {
	tag, err := language.Parse(strings.ReplaceAll(strings.TrimSpace(s), "_", "-"))
	if err != nil {
		return language.Und, err
	}
	if tag == language.Und {
		return language.Und, errors.New("language: undetermined language tag")
	}
	return tag, nil
}
