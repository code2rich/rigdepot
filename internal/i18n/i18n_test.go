// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package i18n

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
	"github.com/code2rich/rigdepot/locales"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// sample is a small catalogue in the real layout: English, Simplified
// Chinese, and a third language that is not complete.
func sample() fstest.MapFS {
	return fstest.MapFS{
		"en/meta.json": {Data: []byte(`{"name": "English"}`)},
		"en/cli.json": {Data: []byte(`{
  "files.total_one": "{{count}} file in {{dir}}",
  "files.total_other": "{{count}} files in {{dir}}",
  "greeting": "Hello, {{name}}",
  "only.english": "Only in English",
  "run.hint": "Run {{cli}} sync"
}`)},
		"en/check.json":   {Data: []byte(`{"check.structure.missing": "{{file}} is missing"}`)},
		"zh-CN/meta.json": {Data: []byte(`{"name": "简体中文"}`)},
		"zh-CN/cli.json": {Data: []byte(`{
  "files.total_one": "{{dir}} 里有 {{count}} 个文件",
  "files.total_other": "{{dir}} 里有 {{count}} 个文件",
  "greeting": "你好，{{name}}",
  "run.hint": "运行 {{cli}} sync"
}`)},
		"de/cli.json": {Data: []byte(`{"greeting": "Hallo, {{name}}"}`)},
		"README.md":   {Data: []byte("not a language")},
	}
}

func load(t *testing.T, fsys fstest.MapFS, domains ...string) *Catalog {
	t.Helper()
	if len(domains) == 0 {
		domains = []string{"cli", "check"}
	}
	c, err := Load(fsys, domains...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoadFindsLanguagesFromDirectories(t *testing.T) {
	c := load(t, sample())
	var tags, names []string
	for _, l := range c.Languages() {
		tags = append(tags, l.Tag.String())
		names = append(names, l.Name)
	}
	if want := []string{"en", "de", "zh-CN"}; !slices.Equal(tags, want) {
		t.Errorf("languages = %v, want %v (English first, then by tag)", tags, want)
	}
	if want := []string{"English", "de", "简体中文"}; !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v (from meta.json, the tag without one)", names, want)
	}
}

func TestLoadRejectsBrokenCatalogues(t *testing.T) {
	tests := map[string]func(fstest.MapFS){
		"a file that is not JSON": func(m fstest.MapFS) { m["zh-CN/cli.json"] = &fstest.MapFile{Data: []byte(`{"greeting": `)} },
		"a value that is not text": func(m fstest.MapFS) {
			m["de/cli.json"] = &fstest.MapFile{Data: []byte(`{"greeting": {"nested": "x"}}`)}
		},
		"a key in two files":         func(m fstest.MapFS) { m["en/check.json"] = &fstest.MapFile{Data: []byte(`{"greeting": "again"}`)} },
		"no English":                 func(m fstest.MapFS) { delete(m, "en/meta.json"); delete(m, "en/cli.json"); delete(m, "en/check.json") },
		"a directory that is no tag": func(m fstest.MapFS) { m["english/cli.json"] = &fstest.MapFile{Data: []byte(`{}`)} },
		"a tag that is not canonical": func(m fstest.MapFS) {
			m["zh_CN/cli.json"] = &fstest.MapFile{Data: []byte(`{}`)}
		},
		"a broken meta.json": func(m fstest.MapFS) { m["de/meta.json"] = &fstest.MapFile{Data: []byte(`{`)} },
	}
	for name, breakIt := range tests {
		t.Run(name, func(t *testing.T) {
			fsys := sample()
			breakIt(fsys)
			if _, err := Load(fsys, "cli", "check"); err == nil {
				t.Error("Load succeeded")
			}
		})
	}
}

func TestMatch(t *testing.T) {
	c := load(t, sample())
	tests := []struct {
		prefs []string
		want  string
	}{
		{nil, "en"},
		{[]string{"zh-CN"}, "zh-CN"},
		{[]string{"zh"}, "zh-CN"},
		{[]string{"zh-Hans"}, "zh-CN"},
		{[]string{"zh-SG"}, "zh-CN"},
		{[]string{"zh_CN"}, "zh-CN"},
		{[]string{"en-GB"}, "en"},
		{[]string{"de-AT"}, "de"},
		{[]string{"fr"}, "en"},
		{[]string{"fr", "zh-CN"}, "zh-CN"}, // the first preference that matches wins
		{[]string{"zh-CN", "de"}, "zh-CN"},
		{[]string{"", "C", "not a tag!", "de"}, "de"},
	}
	for _, tt := range tests {
		if got := c.Match(tt.prefs...).String(); got != tt.want {
			t.Errorf("Match(%q) = %s, want %s", tt.prefs, got, tt.want)
		}
	}
}

// Until there is a zh-TW catalogue, readers of Traditional Chinese get
// Simplified Chinese, not English.
func TestTraditionalChineseFallsBackToSimplified(t *testing.T) {
	c := load(t, sample())
	for _, pref := range []string{"zh-TW", "zh-HK", "zh-MO", "zh-Hant", "zh-Hant-TW", "zh-Hant-HK", "zh-Bopo", "zh_TW.UTF-8"} {
		tag := pref
		if i := strings.Index(tag, "."); i >= 0 {
			tag = tag[:i]
		}
		if got := c.Match(tag).String(); got != "zh-CN" {
			t.Errorf("Match(%q) = %s, want zh-CN", pref, got)
		}
		if got := c.Match("fr", tag).String(); got != "zh-CN" {
			t.Errorf("Match(fr, %q) = %s, want zh-CN", pref, got)
		}
	}
}

func TestT(t *testing.T) {
	c := load(t, sample())
	en, zh, de := c.Printer(c.Match("en")), c.Printer(c.Match("zh-CN")), c.Printer(c.Match("de"))
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"placeholder", en.T("greeting", V("name", "Kim")), "Hello, Kim"},
		{"placeholder in Chinese", zh.T("greeting", V("name", "Kim")), "你好，Kim"},
		{"English plural one", en.T("files.total", Count(1), V("dir", "skills")), "1 file in skills"},
		{"English plural other", en.T("files.total", Count(4), V("dir", "skills")), "4 files in skills"},
		{"English plural zero", en.T("files.total", Count(0), V("dir", "skills")), "0 files in skills"},
		{"Chinese has only other", zh.T("files.total", Count(1), V("dir", "skills")), "skills 里有 1 个文件"},
		{"missing in Chinese falls back to English", zh.T("only.english"), "Only in English"},
		{"missing in a partial language falls back", de.T("files.total", Count(2), V("dir", "x")), "2 files in x"},
		{"partial language has its own", de.T("greeting", V("name", "Kim")), "Hallo, Kim"},
		{"missing everywhere shows the key", en.T("no.such.key"), "no.such.key"},
		{"unfilled placeholder stays visible", en.T("greeting"), "Hello, {{name}}"},
		{"brand names need no argument", en.T("run.hint"), "Run " + brand.CLI + " sync"},
		{"an argument wins over a brand name", en.T("run.hint", V("cli", "x")), "Run x sync"},
		{"second domain", en.T("check.structure.missing", V("file", "SKILL.md")), "SKILL.md is missing"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

func TestOnlyRequestedDomainsAreLoaded(t *testing.T) {
	c := load(t, sample(), "cli")
	if got := c.Printer(c.Match("en")).T("check.structure.missing"); got != "check.structure.missing" {
		t.Errorf("a message of a domain that was not loaded rendered as %q", got)
	}
}

func TestContext(t *testing.T) {
	c := load(t, sample())
	p := c.Printer(c.Match("zh-CN"))
	if got := From(NewContext(context.Background(), p)); got != p {
		t.Error("From does not return the printer put into the context")
	}
	if got := From(context.Background()).T("greeting"); got != "greeting" {
		t.Errorf("without a printer, T = %q; want the key, so that the defect shows", got)
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]string{"zh-CN": "zh-CN", "zh_CN": "zh-CN", " en ": "en", "zh-Hant-TW": "zh-Hant-TW"} {
		if tag, err := Parse(in); err != nil || tag.String() != want {
			t.Errorf("Parse(%q) = %v, %v; want %s", in, tag, err, want)
		}
	}
	for _, in := range []string{"", "und", "C", "english!"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) succeeded", in)
		}
	}
}

func TestEmbeddedCataloguesLoad(t *testing.T) {
	c, err := Load(locales.FS, "catalog", "check", "cli")
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, l := range c.Languages() {
		tags = append(tags, l.Tag.String())
	}
	for _, want := range []string{"en", "zh-CN"} {
		if !slices.Contains(tags, want) {
			t.Errorf("embedded languages %v lack %s", tags, want)
		}
	}
}

func TestEnvLanguages(t *testing.T) {
	tests := []struct {
		env  map[string]string
		want []string
	}{
		{map[string]string{}, nil},
		{map[string]string{"LANG": "zh_CN.UTF-8"}, []string{"zh-CN"}},
		{map[string]string{"LANG": "de_DE@euro"}, []string{"de-DE"}},
		{map[string]string{"LANG": "C.UTF-8"}, nil},
		{map[string]string{"LANG": "POSIX", "LANGUAGE": "fr:en"}, []string{"fr", "en"}},
		{map[string]string{"LC_ALL": "C", "LC_MESSAGES": "ja_JP.UTF-8", "LANG": "en_US.UTF-8"}, []string{"ja-JP"}},
		{map[string]string{"LC_ALL": "zh_TW.UTF-8", "LANG": "en_US.UTF-8"}, []string{"zh-TW"}},
		{map[string]string{"LANG": "en_US.UTF-8", "LANGUAGE": "zh_CN"}, []string{"en-US"}},
	}
	for _, tt := range tests {
		got := envLanguages(func(k string) string { return tt.env[k] })
		if !slices.Equal(got, tt.want) {
			t.Errorf("envLanguages(%v) = %v, want %v", tt.env, got, tt.want)
		}
	}
}

func TestDarwinLanguages(t *testing.T) {
	plist := []byte("(\n    \"zh-Hans-CN\",\n    en,\n    \"ja-JP\"\n)\n")
	noEnv := func(string) string { return "" }
	apple := func() ([]byte, error) { return plist, nil }
	if got, want := darwinLanguages(noEnv, apple), []string{"zh-Hans-CN", "en", "ja-JP"}; !slices.Equal(got, want) {
		t.Errorf("from AppleLanguages: %v, want %v", got, want)
	}
	withEnv := func(k string) string {
		if k == "LANG" {
			return "en_GB.UTF-8"
		}
		return ""
	}
	if got := darwinLanguages(withEnv, func() ([]byte, error) { t.Error("AppleLanguages read although LANG is set"); return nil, nil }); !slices.Equal(got, []string{"en-GB"}) {
		t.Errorf("with LANG: %v", got)
	}
	failing := func() ([]byte, error) { return nil, errors.New("defaults: no such key") }
	if got := darwinLanguages(noEnv, failing); len(got) != 0 {
		t.Errorf("when defaults fails: %v, want nothing", got)
	}
	if got := parseAppleLanguages([]byte("(\n)\n")); len(got) != 0 {
		t.Errorf("empty array: %v", got)
	}
}
