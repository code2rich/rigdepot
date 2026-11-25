// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parse turns a map of path -> content into parsed Go files, failing the test
// when one does not parse.
func parse(t *testing.T, sources map[string]string) []goFile {
	t.Helper()
	var files []sourceFile
	for rel, content := range sources {
		files = append(files, sourceFile{rel: rel, data: []byte(content)})
	}
	parsed, err := parseGo(files)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func summarize(violations []violation) string {
	var lines []string
	for _, v := range violations {
		lines = append(lines, v.Rule+": "+v.From+" -> "+v.To)
	}
	return strings.Join(lines, "\n")
}

const checkRule = `package bundledenv

import "example.test/depot/ext/check"

func New() check.Rule { return nil }
`

func TestRegistration(t *testing.T) {
	registered := `package all

import (
	"example.test/depot/builtin/checks/bundledenv"
	"example.test/depot/ext/check"
)

func init() { check.Register(bundledenv.New()) }
`
	importedOnly := `package all

import _ "example.test/depot/builtin/checks/bundledenv"
`
	t.Run("registered", func(t *testing.T) {
		got := checkRegistration(testModule, parse(t, map[string]string{
			"builtin/checks/bundledenv/rule.go": checkRule,
			"builtin/all/checks.go":             registered,
		}))
		if len(got) != 0 {
			t.Errorf("unexpected violations: %v", got)
		}
	})
	t.Run("a directory without New has nothing to register yet", func(t *testing.T) {
		got := checkRegistration(testModule, parse(t, map[string]string{
			"builtin/checks/bundledenv/doc.go": "// Package bundledenv is a placeholder.\npackage bundledenv\n",
			"builtin/checks/internal/text.go":  "package internal\n\nfunc New() int { return 0 }\n",
		}))
		if len(got) != 0 {
			t.Errorf("unexpected violations: %v", got)
		}
	})
	for name, registry := range map[string]string{"not imported": "package all\n", "imported but New is never called": importedOnly} {
		t.Run(name, func(t *testing.T) {
			got := checkRegistration(testModule, parse(t, map[string]string{
				"builtin/checks/bundledenv/rule.go": checkRule,
				"builtin/all/checks.go":             registry,
			}))
			if summarize(got) != "registration: builtin/checks/bundledenv -> builtin/all" {
				t.Fatalf("violations = %v, want one for the unregistered rule", got)
			}
			const fix = "builtin/all/checks.go: check.Register(bundledenv.New())"
			if !strings.Contains(got[0].Why, fix) {
				t.Errorf("explanation = %q, want it to say where to add which line (%q)", got[0].Why, fix)
			}
		})
	}
	t.Run("a server-only item belongs in the server registry", func(t *testing.T) {
		got := checkRegistration(testModule, parse(t, map[string]string{
			"builtin/notifiers/webhook/channel.go": "package webhook\n\nfunc New() int { return 0 }\n",
			"builtin/all/notifiers.go":             "package all\n\nimport \"example.test/depot/builtin/notifiers/webhook\"\n\nvar _ = webhook.New()\n",
		}))
		if summarize(got) != "registration: builtin/notifiers/webhook -> builtin/all/server" {
			t.Errorf("violations = %v, want one pointing at builtin/all/server", got)
		}
	})
	t.Run("a module is registered in its own build-tagged file", func(t *testing.T) {
		got := checkRegistration(testModule, parse(t, map[string]string{
			"modules/insights/module.go": "package insights\n\nfunc New() int { return 0 }\n",
		}))
		if len(got) != 1 || !strings.Contains(got[0].Why, "modules/all/maybe_insights.go: module.Register(insights.New())") {
			t.Errorf("violations = %v, want one naming modules/all/maybe_insights.go", got)
		}
	})
}

func TestTestIsolation(t *testing.T) {
	const isolated = `package home

import (
	"testing"

	"example.test/depot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }
`
	const viaSuite = `package claudecode

import (
	"testing"

	"example.test/depot/ext/adapter/adaptertest"
)

func TestMain(m *testing.M) { adaptertest.Main(m) }
`
	const ownSetup = `package e2e

import (
	"os"
	"testing"

	isolation "example.test/depot/internal/testenv"
)

func TestMain(m *testing.M) { os.Exit(isolation.Run(m, build)) }
`
	const noTestMain = "package home\n\nimport \"testing\"\n\nfunc TestLayout(t *testing.T) {}\n"
	const unrelatedTestMain = `package home

import (
	"os"
	"testing"

	_ "example.test/depot/internal/testenv"
)

func TestMain(m *testing.M) { os.Exit(m.Run()) }
`
	got := checkTestIsolation(testModule, parse(t, map[string]string{
		"internal/home/main_test.go":              isolated,
		"internal/home/layout_test.go":            noTestMain, // covered by main_test.go in the same directory
		"builtin/adapters/claudecode/a_test.go":   viaSuite,
		"test/e2e/e2e_test.go":                    ownSetup,
		"internal/manifest/manifest_test.go":      noTestMain,
		"internal/executor/executor_test.go":      unrelatedTestMain,
		"internal/sync/sync.go":                   "package sync\n",
		"internal/sync/testdata/sample/x_test.go": noTestMain, // a fixture, not a test of ours
		"internal/brand/brand_gen.go":             "// Code generated by a tool. DO NOT EDIT.\n\npackage brand\n",
	}))
	want := "test isolation: internal/executor -> internal/testenv\ntest isolation: internal/manifest -> internal/testenv"
	if summarize(sorted(got)) != want {
		t.Errorf("violations:\n%s\nwant:\n%s", summarize(sorted(got)), want)
	}
}

func sorted(violations []violation) []violation {
	out := append([]violation(nil), violations...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j].String() < out[i].String() {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestBrandLiterals(t *testing.T) {
	names := brandNames{Name: "Acme", Prefix: "acm-", Home: ".acme"}
	got := checkBrandLiterals(names, parse(t, map[string]string{
		"internal/home/home.go": `package home

import "example.test/Acme/internal/brand" // an import path is not a name

// Acme keeps its files in .acme; comments may say so.
const dir = ".acme"

var server = "acm-server"

func banner() string { return "Welcome to " + brand.Name }
`,
		"internal/home/home_test.go":        "package home\n\nvar want = `managed-by: Acme`\n",
		"internal/home/testdata/fixture.go": "package fixture\n\nvar x = \"Acme\"\n",
		"internal/brand/brand_gen.go":       "// Code generated by a tool. DO NOT EDIT.\n\npackage brand\n\nconst Name = \"Acme\"\n",
		"internal/cliout/unrelated.go":      "package cliout\n\nvar x = \"acme\" // lower case is not the product name\n",
	}))
	want := strings.Join([]string{
		`brand: internal/home/home.go:6 -> ".acme"`,
		`brand: internal/home/home.go:8 -> "acm-"`,
		`brand: internal/home/home_test.go:3 -> "Acme"`,
	}, "\n")
	if summarize(sorted(got)) != want {
		t.Errorf("violations:\n%s\nwant:\n%s", summarize(sorted(got)), want)
	}
}

func TestBuiltinsDoNotWriteFiles(t *testing.T) {
	got := checkNoWrites(parse(t, map[string]string{
		"builtin/adapters/codex/adapter.go": `package codex

import (
	"os"
	sys "os"
)

func plan() {
	os.ReadFile("config.toml")              // reading is fine
	sys.WriteFile("config.toml", nil, 0o600) // writing is the executor's job
}
`,
		"builtin/adapters/codex/adapter_test.go": "package codex\n\nimport \"os\"\n\nfunc fixture() { os.MkdirAll(\"x\", 0o700) }\n",
		"internal/executor/write.go":             "package executor\n\nimport \"os\"\n\nfunc write() { os.Rename(\"a\", \"b\") }\n",
	}))
	if summarize(got) != "R7: builtin/adapters/codex -> os.WriteFile" {
		t.Errorf("violations = %v, want exactly the write in the adapter", got)
	}
}

func TestLicenseHeaders(t *testing.T) {
	const header = "// SPDX-License-Identifier: Apache-2.0\n// Copyright 2026 The Example Authors\n\n"
	files := []sourceFile{
		{rel: "internal/home/home.go", data: []byte(header + "package home\n")},
		{rel: "internal/home/bare.go", data: []byte("package home\n")},
		{rel: "internal/brand/brand_gen.go", data: []byte("// Code generated by a tool. DO NOT EDIT.\n\npackage brand\n")},
		{rel: "scripts/install.sh", data: []byte("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\nset -eu\n")},
		{rel: "scripts/bare.ps1", data: []byte("$ErrorActionPreference = 'Stop'\n")},
		{rel: "deploy/compose.yaml", data: []byte("services: {}\n")},
		{rel: "Dockerfile", data: []byte("FROM scratch\n")},
		{rel: "late.go", data: []byte("package late\n\n\n\n\n\n// SPDX-License-Identifier: Apache-2.0\n")},
		{rel: "README.md", data: []byte("# Example\n")},
		{rel: "brand.json", data: []byte("{}\n")},
		{rel: "builtin/adapters/codex/testdata/homes/empty/.codex/config.toml", data: []byte("model = \"x\"\n")},
	}
	want := strings.Join([]string{
		"license header: Dockerfile -> " + licenseLine,
		"license header: deploy/compose.yaml -> " + licenseLine,
		"license header: internal/home/bare.go -> " + licenseLine,
		"license header: late.go -> " + licenseLine,
		"license header: scripts/bare.ps1 -> " + licenseLine,
	}, "\n")
	if got := summarize(sorted(checkLicenseHeaders(files))); got != want {
		t.Errorf("violations:\n%s\nwant:\n%s", got, want)
	}
}

func TestWalkSourceSkipsBuildOutput(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"main.go", "dist/main.go", ".dev/home/x.go", "web/node_modules/pkg/index.ts", "internal/a/testdata/f.json"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := walkSource(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.rel)
	}
	if want := "internal/a/testdata/f.json main.go"; strings.Join(got, " ") != want {
		t.Errorf("files = %v, want %q", got, want)
	}
}

func TestModuleTags(t *testing.T) {
	const good = "// SPDX-License-Identifier: Apache-2.0\n\n//go:build !rgd_omit_ai_review && !rgd_core_only\n\npackage all\n"
	for name, tc := range map[string]struct {
		rel, src string
		bad      bool
	}{
		"correct":                     {"modules/all/maybe_ai-review.go", good, false},
		"doc.go needs no tag":         {"modules/all/doc.go", "package all\n", false},
		"tests need no tag":           {"modules/all/all_test.go", "package all\n", false},
		"no constraint":               {"modules/all/maybe_insights.go", "package all\n", true},
		"only the omit tag":           {"modules/all/maybe_insights.go", "//go:build !rgd_omit_insights\n\npackage all\n", true},
		"another module's tag":        {"modules/all/maybe_insights.go", "//go:build !rgd_omit_review && !rgd_core_only\n\npackage all\n", true},
		"a file that is not maybe_*":  {"modules/all/register.go", "package all\n", true},
		"constraint after the clause": {"modules/all/maybe_insights.go", "package all\n\n//go:build !rgd_omit_insights && !rgd_core_only\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			got := checkModuleTags([]sourceFile{{rel: tc.rel, data: []byte(tc.src)}})
			if tc.bad != (len(got) == 1) || len(got) > 1 {
				t.Errorf("violations = %v, want bad=%v", got, tc.bad)
			}
		})
	}
}
