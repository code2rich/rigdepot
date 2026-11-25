// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// webTree is a repository with the web console: the brand, a package.json,
// two tokens and the contrast rule.
func webTree() map[string]string {
	return map[string]string{
		brandFile:      sampleBrand,
		webPackageFile: `{"name": "console"}`,
		tokensFile: `{
  "description": "test",
  "groups": [
    { "name": "surface", "description": "Pages.", "tokens": [
      { "name": "bg", "light": "#ffffff", "dark": "#000000" }
    ]},
    { "name": "ink", "description": "Text.", "tokens": [
      { "name": "ink", "light": "#111111", "dark": "#eeeeee" },
      { "name": "accent", "value": "#ef4a12" }
    ]},
    { "name": "space", "description": "Spacing.", "tokens": [{ "name": "s1", "value": "4px" }]}
  ],
  "contrast": { "description": "", "min": 4.5, "text": ["ink"], "surfaces": ["bg"] }
}`,
	}
}

func generatedFile(t *testing.T, files []generated, path string) string {
	t.Helper()
	for _, f := range files {
		if f.path == path {
			return string(f.data)
		}
	}
	t.Fatalf("%s was not generated", path)
	return ""
}

func TestGenWritesTheConsoleNamesAndTokens(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no Node: only the API types are skipped
	files, skipped, err := generate(writeTree(t, webTree()), nil)
	if err != nil {
		t.Fatal(err)
	}
	brand := generatedFile(t, files, brandTSOutput)
	for _, want := range []string{licenseLine, "DO NOT EDIT.", `name: "Acme"`, `cli: "acm"`, `cliLong: "acme"`, `envPrefix: "ACM_"`, "} as const"} {
		if !strings.Contains(brand, want) {
			t.Errorf("%s lacks %q:\n%s", brandTSOutput, want, brand)
		}
	}
	css := generatedFile(t, files, tokensOutput)
	light, rest, _ := strings.Cut(css, "@media (prefers-color-scheme: dark)")
	system, manual, _ := strings.Cut(rest, `:root[data-theme="dark"]`)
	for _, want := range []string{licenseLine, "--bg: #ffffff;", "--ink: #111111;", "--accent: #ef4a12;", "--s1: 4px;", "color-scheme: light;"} {
		if !strings.Contains(light, want) {
			t.Errorf("the light theme lacks %q:\n%s", want, light)
		}
	}
	// Both dark blocks: the system preference unless light was picked, and dark picked by hand.
	if !strings.Contains(system, `:root:not([data-theme="light"])`) {
		t.Errorf("the system dark theme does not yield to a manual light choice:\n%s", system)
	}
	for _, block := range []string{system, manual} {
		for _, want := range []string{"--bg: #000000;", "--ink: #eeeeee;", "color-scheme: dark;"} {
			if !strings.Contains(block, want) {
				t.Errorf("a dark block lacks %q:\n%s", want, block)
			}
		}
		if strings.Contains(block, "--accent") || strings.Contains(block, "--s1") {
			t.Errorf("a dark block repeats a token that has one value:\n%s", block)
		}
	}
	if len(skipped) != 0 {
		t.Errorf("skipped %v without a contract to generate from", skipped)
	}
}

func TestTokenProblems(t *testing.T) {
	tests := map[string]struct{ from, to, want string }{
		"faint text":       {`"light": "#111111"`, `"light": "#aaaaaa"`, "ink on bg in the light theme has a contrast of 2.32 : 1"},
		"faint dark text":  {`"dark": "#eeeeee"`, `"dark": "#333333"`, "ink on bg in the dark theme"},
		"half a theme":     {`, "dark": "#eeeeee"`, ``, `token "ink" needs a value, or both`},
		"both forms":       {`{ "name": "accent", "value": "#ef4a12" }`, `{ "name": "accent", "value": "#ef4a12", "dark": "#000000" }`, "a value and theme values"},
		"duplicate":        {`{ "name": "s1", "value": "4px" }`, `{ "name": "s1", "value": "4px" }, { "name": "s1", "value": "8px" }`, `token "s1" appears twice`},
		"bad name":         {`"name": "s1"`, `"name": "S 1"`, "is not a token name"},
		"css injection":    {`"value": "4px"`, `"value": "4px; } body { x: y"`, "not a single CSS value"},
		"unknown contrast": {`"text": ["ink"]`, `"text": ["ink", "missing"]`, `contrast names "missing"`},
		"not hex":          {`"surfaces": ["bg"]`, `"surfaces": ["bg", "accent", "s1"]`, `"4px" is not a #rrggbb colour`},
		"unknown field":    {`"description": "test",`, `"description": "test", "colour": 1,`, "unknown field"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tree := webTree()
			if !strings.Contains(tree[tokensFile], tt.from) {
				t.Fatalf("the sample has no %q", tt.from)
			}
			tree[tokensFile] = strings.Replace(tree[tokensFile], tt.from, tt.to, 1)
			_, _, err := generate(writeTree(t, tree), nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to say %q", err, tt.want)
			}
		})
	}
}

// The tokens of the real console pass the contrast rule: the prototype's
// values reach 4.5 : 1 for every text colour on every surface.
func TestTheConsoleTokensPass(t *testing.T) {
	root, err := findRoot()
	if err != nil {
		t.Fatal(err)
	}
	tf, err := readTokens(root)
	if err != nil {
		t.Fatal(err)
	}
	if tf == nil {
		t.Skip("no " + tokensFile)
	}
}

func TestContrastRatio(t *testing.T) {
	black, _ := parseHexColor("#000000")
	white, _ := parseHexColor("#ffffff")
	if r := contrastRatio(black, white); r < 20.99 || r > 21.01 {
		t.Errorf("black on white = %.3f, want 21", r)
	}
	if r := contrastRatio(white, white); r != 1 {
		t.Errorf("white on white = %.3f, want 1", r)
	}
	grey, _ := parseHexColor("#767676") // the classic lightest grey that passes on white
	if r := contrastRatio(grey, white); r < 4.5 || r > 4.6 {
		t.Errorf("#767676 on white = %.3f, want just over 4.5", r)
	}
}

// Without Node, the API types are reported as not run, never as up to date,
// while everything Go can generate is still produced.
func TestWithoutNodeTheAPITypesAreNotRun(t *testing.T) {
	tree := webTree()
	tree[apiSpec] = "openapi: 3.0.3\n"
	root := writeTree(t, tree)
	t.Setenv("PATH", t.TempDir())
	b, err := readBrand(root)
	if err != nil {
		t.Fatal(err)
	}
	files, skipped, err := generateWeb(root, nil, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0].path != apiTSOutput || !strings.Contains(skipped[0].reason, "no Node") {
		t.Errorf("skipped = %v, want %s not run for lack of Node", skipped, apiTSOutput)
	}
	for _, f := range files {
		if f.path == apiTSOutput {
			t.Errorf("%s was generated without Node", apiTSOutput)
		}
	}
	generatedFile(t, files, brandTSOutput)
	generatedFile(t, files, tokensOutput)
}

// With Node but without the console's dependencies the reason says what to
// run. The test makes a node of its own so that it does not depend on the
// machine.
func TestWithoutTheConsoleDependenciesTheReasonSaysWhatToRun(t *testing.T) {
	tree := webTree()
	tree[apiSpec] = "openapi: 3.0.3\n"
	root := writeTree(t, tree)
	bin := t.TempDir()
	for _, name := range []string{"node", "node.exe"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	b, err := readBrand(root)
	if err != nil {
		t.Fatal(err)
	}
	_, skipped, err := generateWeb(root, nil, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0].reason, "web-deps") {
		t.Errorf("skipped = %v, want a hint to run web-deps", skipped)
	}
}

// gen --check fails on a generated console file that was edited by hand.
func TestGenCheckFindsAnEditedTokenFile(t *testing.T) {
	root := writeTree(t, webTree())
	if _, err := gen(root, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := gen(root, nil, true); err != nil {
		t.Fatalf("gen --check right after gen: %v", err)
	}
	css := filepath.Join(root, filepath.FromSlash(tokensOutput))
	if err := os.WriteFile(css, []byte("edited by hand"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gen(root, nil, true); err == nil || !strings.Contains(err.Error(), tokensOutput) {
		t.Errorf("gen --check after editing %s: err = %v", tokensOutput, err)
	}
}

func TestGenRefusesConsoleOutputsWithoutTheConsole(t *testing.T) {
	for _, out := range []string{brandTSOutput, tokensOutput} {
		tree := webTree()
		tree[out] = "stale"
		delete(tree, webPackageFile)
		if out == tokensOutput {
			tree[webPackageFile] = `{}`
			delete(tree, tokensFile)
		}
		_, _, err := generate(writeTree(t, tree), nil)
		if err == nil || !strings.Contains(err.Error(), "missing") {
			t.Errorf("%s left without its source: err = %v", out, err)
		}
	}
}

func TestTheWebKeysType(t *testing.T) {
	tree := webTree()
	for k, v := range messageTree() {
		tree[k] = v
	}
	tree["locales/en/web.json"] = `{"nav.home": "Home", "items.count_one": "{{count}} item", "items.count_other": "{{count}} items"}` + "\n"
	tree["locales/zh-CN/web.json"] = `{"nav.home": "工作台", "items.count_one": "{{count}} 项", "items.count_other": "{{count}} 项"}` + "\n"
	files, _, err := generate(writeTree(t, tree), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := generatedFile(t, files, webKeysOutput)
	// The console's own keys and the shared check keys, plural forms folded; not the command line's.
	for _, want := range []string{`| "nav.home"`, `| "items.count"`, `| "check.structure.missing"`, licenseLine} {
		if !strings.Contains(ts, want) {
			t.Errorf("%s lacks %s:\n%s", webKeysOutput, want, ts)
		}
	}
	for _, unwanted := range []string{"items.count_one", "greeting", "files.total"} {
		if strings.Contains(ts, unwanted) {
			t.Errorf("%s has %s:\n%s", webKeysOutput, unwanted, ts)
		}
	}
}
