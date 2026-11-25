// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const msgTestModule = "example.com/acmemsg"

// messageTree is a small repository: two required languages that agree,
// and Go code that uses every key, through the generated constants and as
// a string literal (the way built-in implementations do).
func messageTree() map[string]string {
	return map[string]string{
		"locales/en/meta.json":    `{"name": "English"}`,
		"locales/zh-CN/meta.json": `{"name": "简体中文"}`,
		"locales/en/cli.json": `{
  "files.total_one": "{{count}} file in {{dir}}",
  "files.total_other": "{{count}} files in {{dir}}",
  "greeting": "Hello, {{name}}"
}
`,
		"locales/zh-CN/cli.json": `{
  "files.total_one": "{{dir}} 里有 {{count}} 个文件",
  "files.total_other": "{{dir}} 里有 {{count}} 个文件",
  "greeting": "你好，{{name}}"
}
`,
		"locales/en/check.json":    `{"check.structure.missing": "{{file}} is missing"}` + "\n",
		"locales/zh-CN/check.json": `{"check.structure.missing": "缺少 {{file}}"}` + "\n",
		"internal/cli/hello.go": `package cli

import (
	"example.com/acmemsg/internal/i18n"
	"example.com/acmemsg/internal/i18n/msg"
)

func hello(p *i18n.Printer) string {
	return p.T(msg.Greeting) + p.T(msg.FilesTotal, i18n.Count(2))
}
`,
		"builtin/checks/structure/structure.go": `package structure

const missing = "check.structure.missing"
`,
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runMessageCheck(t *testing.T, files map[string]string) (string, error) {
	t.Helper()
	root := writeTree(t, files)
	src, err := walkSource(root)
	if err != nil {
		t.Fatal(err)
	}
	goFiles, err := parseGo(src)
	if err != nil {
		t.Fatal(err)
	}
	return checkMessages(root, msgTestModule, goFiles)
}

func TestMessageCheckPasses(t *testing.T) {
	detail, err := runMessageCheck(t, messageTree())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "en, zh-CN complete (3 keys)") || !strings.Contains(detail, "other languages: none") {
		t.Errorf("detail = %q", detail)
	}
}

// Each broken catalogue fails, and the report names the file and the key.
func TestMessageCheckFindsProblems(t *testing.T) {
	tests := []struct {
		name   string
		change func(map[string]string)
		want   []string
	}{
		{
			"a key missing in zh-CN",
			func(f map[string]string) {
				f["locales/zh-CN/cli.json"] = strings.Replace(f["locales/zh-CN/cli.json"], `,
  "greeting": "你好，{{name}}"`, "", 1)
			},
			[]string{"locales/zh-CN/cli.json", `missing key "greeting"`, `en: "Hello, {{name}}"`},
		},
		{
			"a whole file missing in zh-CN",
			func(f map[string]string) { delete(f, "locales/zh-CN/check.json") },
			[]string{"locales/zh-CN/check.json", `missing key "check.structure.missing"`},
		},
		{
			"a key only in zh-CN",
			func(f map[string]string) {
				f["locales/zh-CN/check.json"] = `{"check.structure.missing": "缺少 {{file}}", "check.zz": "多余"}`
			},
			[]string{"locales/zh-CN/check.json", `key "check.zz" is not in locales/en/check.json`},
		},
		{
			"different placeholders",
			func(f map[string]string) {
				f["locales/zh-CN/cli.json"] = strings.Replace(f["locales/zh-CN/cli.json"], "你好，{{name}}", "你好，{{user}}", 1)
			},
			[]string{"locales/zh-CN/cli.json", `key "greeting"`, "{{user}}", "{{name}}"},
		},
		{
			"a placeholder dropped from a plural form",
			func(f map[string]string) {
				f["locales/zh-CN/cli.json"] = strings.Replace(f["locales/zh-CN/cli.json"], "{{dir}} 里有 {{count}} 个文件\",\n  \"greeting", "有 {{count}} 个文件\",\n  \"greeting", 1)
			},
			[]string{`key "files.total_other"`, "placeholders none, but en has {{dir}}"},
		},
		{
			"a key nobody uses",
			func(f map[string]string) {
				f["locales/en/check.json"] = `{"check.structure.missing": "{{file}} is missing", "check.unused": "x"}`
				f["locales/zh-CN/check.json"] = `{"check.structure.missing": "缺少 {{file}}", "check.unused": "x"}`
			},
			[]string{"locales/en/check.json", `key "check.unused"`, "msg.CheckUnused", "not used"},
		},
		{
			"a key used only by a test",
			func(f map[string]string) {
				f["builtin/checks/structure/structure.go"] = "package structure\n"
				f["builtin/checks/structure/structure_test.go"] = "package structure\n\nconst missing = \"check.structure.missing\"\n"
			},
			[]string{`key "check.structure.missing"`, "not used"},
		},
		{
			"a literal key that does not exist",
			func(f map[string]string) {
				f["internal/cli/typo.go"] = "package cli\n\nimport \"example.com/acmemsg/internal/i18n\"\n\nfunc typo(p *i18n.Printer) string { return p.T(\"greting\") }\n"
			},
			[]string{"internal/cli/typo.go:5", `"greting" does not exist`},
		},
		{
			"a duplicate key",
			func(f map[string]string) {
				f["locales/en/check.json"] = `{"check.structure.missing": "a", "check.structure.missing": "b"}`
			},
			[]string{"locales/en/check.json", `"check.structure.missing" appears twice`},
		},
		{
			"keys out of order",
			func(f map[string]string) {
				f["locales/zh-CN/check.json"] = `{"check.z": "缺少", "check.structure.missing": "缺少 {{file}}"}`
			},
			[]string{"locales/zh-CN/check.json", "out of order"},
		},
		{
			"a nested value",
			func(f map[string]string) {
				f["locales/zh-CN/check.json"] = `{"check.structure.missing": {"text": "缺少 {{file}}"}}`
			},
			[]string{"locales/zh-CN/check.json", "not text"},
		},
		{
			"plural forms without other",
			func(f map[string]string) {
				f["locales/zh-CN/cli.json"] = strings.Replace(f["locales/zh-CN/cli.json"], "files.total_other", "files.total_few", 1)
			},
			[]string{`"files.total" has plural forms but no files.total_other`},
		},
		{
			"zh-CN missing",
			func(f map[string]string) {
				for k := range f {
					if strings.HasPrefix(k, "locales/zh-CN/") {
						delete(f, k)
					}
				}
			},
			[]string{"locales/zh-CN is missing"},
		},
		{
			"no meta.json",
			func(f map[string]string) { delete(f, "locales/zh-CN/meta.json") },
			[]string{"locales/zh-CN/meta.json is missing"},
		},
		{
			"a key in two files",
			func(f map[string]string) {
				f["locales/en/check.json"] = `{"check.structure.missing": "{{file}} is missing", "greeting": "x"}`
				f["locales/zh-CN/check.json"] = `{"check.structure.missing": "缺少 {{file}}", "greeting": "x"}`
			},
			[]string{`key "greeting" is also in`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := messageTree()
			tt.change(files)
			_, err := runMessageCheck(t, files)
			if err == nil {
				t.Fatal("the check passed")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the report lacks %q:\n%v", want, err)
				}
			}
		})
	}
}

// A language that is not required may be incomplete: what it lacks falls
// back to English. Its placeholders must still agree.
func TestOptionalLanguages(t *testing.T) {
	files := messageTree()
	files["locales/de/meta.json"] = `{"name": "Deutsch"}`
	files["locales/de/cli.json"] = `{"files.total_few": "{{count}} Dateien in {{dir}}", "files.total_other": "{{count}} Dateien in {{dir}}", "greeting": "Hallo, {{name}}"}`
	detail, err := runMessageCheck(t, files)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "de 50%") {
		t.Errorf("detail = %q, want the completion of de", detail)
	}
	files["locales/de/cli.json"] = `{"greeting": "Hallo, {{nom}}"}`
	if _, err := runMessageCheck(t, files); err == nil || !strings.Contains(err.Error(), "locales/de/cli.json") {
		t.Errorf("a placeholder mismatch in an optional language: %v", err)
	}
}

func TestMessageCheckNotRunWithoutLocales(t *testing.T) {
	_, err := runMessageCheck(t, map[string]string{"main.go": "package main\n"})
	var notRun errNotRun
	if !errors.As(err, &notRun) {
		t.Errorf("err = %v, want not run", err)
	}
}

func TestGenSortsCataloguesAndWritesConstants(t *testing.T) {
	files := messageTree()
	files["brand.json"] = sampleBrand
	files["locales/zh-CN/check.json"] = `{"check.structure.missing":"缺少 <{{file}}> & 别的"}`
	files["locales/en/cli.json"] = `{"greeting": "Hello, {{name}}", "files.total_other": "{{count}} files in {{dir}}", "files.total_one": "{{count}} file in {{dir}}", "config.reason.url": "use a URL"}`
	root := writeTree(t, files)
	if err := runGen(root, nil); err != nil {
		t.Fatal(err)
	}
	sorted, _ := os.ReadFile(filepath.Join(root, "locales", "en", "cli.json"))
	want := `{
  "config.reason.url": "use a URL",
  "files.total_one": "{{count}} file in {{dir}}",
  "files.total_other": "{{count}} files in {{dir}}",
  "greeting": "Hello, {{name}}"
}
`
	if string(sorted) != want {
		t.Errorf("sorted file:\n%s\nwant:\n%s", sorted, want)
	}
	if zh, _ := os.ReadFile(filepath.Join(root, "locales", "zh-CN", "check.json")); !strings.Contains(string(zh), "缺少 <{{file}}> & 别的") {
		t.Errorf("text was escaped:\n%s", zh)
	}
	keys, err := os.ReadFile(filepath.Join(root, "internal", "i18n", "msg", "keys_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DO NOT EDIT.", "package msg", `ConfigReasonURL = "config.reason.url"`, `FilesTotal = "files.total"`, `Greeting = "greeting"`, `CheckStructureMissing = "check.structure.missing"`} {
		if !strings.Contains(string(keys), want) {
			t.Errorf("keys_gen.go lacks %q:\n%s", want, keys)
		}
	}
	if strings.Contains(string(keys), "FilesTotalOne") {
		t.Error("a plural form got its own constant")
	}
	if err := runGen(root, []string{"--check"}); err != nil {
		t.Errorf("gen --check right after gen: %v", err)
	}
}

func TestConstantNameClash(t *testing.T) {
	files := messageTree()
	files["brand.json"] = sampleBrand
	files["locales/en/check.json"] = `{"a.b_c": "x", "a_b.c": "y", "check.structure.missing": "{{file}} is missing"}`
	if _, _, err := generate(writeTree(t, files), nil); err == nil || !strings.Contains(err.Error(), "ABC") {
		t.Errorf("err = %v, want the clash on ABC", err)
	}
}

// webMessageTree adds the console: its own catalogue and TypeScript that
// uses every key, written out in full.
func webMessageTree() map[string]string {
	files := messageTree()
	files["locales/en/web.json"] = `{"items.count_one": "{{count}} item", "items.count_other": "{{count}} items", "nav.home": "Home"}` + "\n"
	files["locales/zh-CN/web.json"] = `{"items.count_one": "{{count}} 项", "items.count_other": "{{count}} 项", "nav.home": "工作台"}` + "\n"
	files["web/src/shell.tsx"] = "const label = t('nav.home')\nconst n = t(\"items.count\", { count: 2 })\n"
	return files
}

func TestMessageCheckSeesTheConsole(t *testing.T) {
	detail, err := runMessageCheck(t, webMessageTree())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "2 console keys, each used in web/src") {
		t.Errorf("detail = %q", detail)
	}
}

func TestMessageCheckFindsUnusedConsoleKeys(t *testing.T) {
	tests := map[string]func(map[string]string){
		"not used at all": func(f map[string]string) {
			f["web/src/shell.tsx"] = "const n = t('items.count', { count: 2 })\n"
		},
		"used only by a test": func(f map[string]string) {
			f["web/src/shell.tsx"] = "const n = t('items.count', { count: 2 })\n"
			f["web/src/shell.test.tsx"] = "expect(t('nav.home')).toBe('Home')\n"
		},
		"used only by a generated file": func(f map[string]string) {
			f["web/src/shell.tsx"] = "const n = t('items.count', { count: 2 })\n"
			f["web/src/i18n/keys.gen.ts"] = "export type MessageKey = \"nav.home\"\n"
		},
		"assembled from parts": func(f map[string]string) {
			f["web/src/shell.tsx"] = "const n = t('items.count', { count: 2 })\nconst page = 'home'\nconst label = t(`nav.${page}`)\n"
		},
		"no console sources": func(f map[string]string) {
			delete(f, "web/src/shell.tsx")
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			files := webMessageTree()
			change(files)
			_, err := runMessageCheck(t, files)
			if err == nil || !strings.Contains(err.Error(), `key "nav.home" is not used anywhere in web/src`) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// A key of a shared catalogue may be used by the console alone.
func TestSharedKeysUsedByTheConsoleCount(t *testing.T) {
	files := webMessageTree()
	delete(files, "builtin/checks/structure/structure.go")
	if _, err := runMessageCheck(t, files); err == nil {
		t.Fatal("check.structure.missing is used by nobody, but the check passed")
	}
	files["web/src/checks.tsx"] = "const text = t('check.structure.missing', { file: 'x' })\n"
	if _, err := runMessageCheck(t, files); err != nil {
		t.Errorf("used by the console only: %v", err)
	}
}

func TestScanTSStrings(t *testing.T) {
	src := "// it's a comment with 'quotes'\n" +
		"/* and \"more\" */\n" +
		"const a = t('nav.home') // done\n" +
		"const b = `${t(page.label)} · ${BRAND.name}`\n" +
		"const c = { x: \"shell.skip\" }\n" +
		"const d = `plain template`\n" +
		"const e = `nested ${f(`inner`, { y: 'deep' })} end`\n" +
		"const f = 'it\\'s'\n" +
		"const g = 'unclosed\n" +
		"t('next.line')\n"
	got := strings.Join(scanTSStrings(src), "|")
	for _, must := range []string{"nav.home", "shell.skip", "plain template", "inner", "deep", "it's", "next.line"} {
		if !strings.Contains("|"+got+"|", "|"+must+"|") {
			t.Errorf("missed %q in %q", must, got)
		}
	}
	for _, mustNot := range []string{"quotes", "more", "nested ", " · "} {
		if strings.Contains(got, mustNot) {
			t.Errorf("saw %q in %q", mustNot, got)
		}
	}
}
