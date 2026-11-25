// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleBrand = `{
  "name": "Acme", "cli": "acm", "cliLong": "acme", "server": "acm-server", "home": ".acme",
  "prefix": "acm-", "managedMarker": "managed-by: Acme", "mcpEntry": "acme",
  "repoConfig": "acme.yaml", "envPrefix": "ACM_"
}`

func writeBrand(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, brandFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestGenWritesConstantsFromBrandFile(t *testing.T) {
	root := writeBrand(t, sampleBrand)
	if err := runGen(root, []string{"--check"}); err == nil {
		t.Fatal("gen --check passed although nothing has been generated")
	}
	if err := runGen(root, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "internal", "brand", "brand_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DO NOT EDIT.", "package brand", `Name = "Acme"`, `Server = "acm-server"`, `Prefix = "acm-"`, `EnvPrefix = "ACM_"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("generated file lacks %q:\n%s", want, got)
		}
	}
	if err := runGen(root, []string{"--check"}); err != nil {
		t.Errorf("gen --check right after gen: %v", err)
	}
}

func TestGenCheckNoticesAStaleFile(t *testing.T) {
	root := writeBrand(t, sampleBrand)
	if err := runGen(root, nil); err != nil {
		t.Fatal(err)
	}
	renamed := strings.Replace(sampleBrand, `"name": "Acme"`, `"name": "Apex"`, 1)
	if err := os.WriteFile(filepath.Join(root, brandFile), []byte(renamed), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runGen(root, []string{"--check"})
	if err == nil || !strings.Contains(err.Error(), "internal/brand/brand_gen.go") {
		t.Errorf("gen --check after a rename: err = %v, want it to name the stale file", err)
	}
}

func TestBrandFileProblemsAreErrors(t *testing.T) {
	tests := map[string]string{
		"a missing name":   strings.Replace(sampleBrand, `"prefix": "acm-", `, "", 1),
		"an empty name":    strings.Replace(sampleBrand, `"home": ".acme"`, `"home": ""`, 1),
		"an unknown name":  strings.Replace(sampleBrand, `"name": "Acme"`, `"name": "Acme", "nickname": "ac"`, 1),
		"not JSON":         "name: Acme",
		"an empty file":    "",
		"the wrong type":   strings.Replace(sampleBrand, `"cli": "acm"`, `"cli": 3`, 1),
		"an array instead": "[]",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := generate(writeBrand(t, content), nil); err == nil {
				t.Error("generate succeeded")
			}
		})
	}
	if _, _, err := generate(t.TempDir(), nil); err == nil {
		t.Error("generate succeeded without a brand file")
	}
}

func TestGenWithoutTheContractGeneratesOnlyTheBrand(t *testing.T) {
	files, _, err := generate(writeBrand(t, sampleBrand), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].path != "internal/brand/brand_gen.go" {
		t.Errorf("generated %v, want only the brand constants", files)
	}
}

func TestGenRefusesOutputsWithoutTheirContract(t *testing.T) {
	root := writeBrand(t, sampleBrand)
	out := filepath.Join(root, filepath.FromSlash(apiTargets[1].output))
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("package apiclient\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := generate(root, nil)
	if err == nil || !strings.Contains(err.Error(), apiSpec) {
		t.Errorf("generate with an orphaned %s: err = %v, want it to name %s", apiTargets[1].output, err, apiSpec)
	}
}

func TestGenCollectsTheKindSchemas(t *testing.T) {
	root := writeBrand(t, sampleBrand)
	schema := `{"$id": "https://example.com/schema/repo/v1/kinds/note.schema.json", "type": "object"}` + "\n"
	dir := filepath.Join(root, "builtin", "kinds", "note")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schema.json"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGen(root, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "schema", "repo", "v1", "kinds", "note.schema.json"))
	if err != nil || string(got) != schema {
		t.Fatalf("the copy is %q, %v; want the schema byte for byte", got, err)
	}

	// The original changes and the copy does not: --check says which file is stale.
	if err := os.WriteFile(filepath.Join(dir, "schema.json"), []byte(strings.Replace(schema, "object", "array", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGen(root, []string{"--check"}); err == nil || !strings.Contains(err.Error(), "schema/repo/v1/kinds/note.schema.json") {
		t.Errorf("gen --check after the schema changed: %v", err)
	}

	// A schema whose $id is not where it is published.
	if err := os.WriteFile(filepath.Join(dir, "schema.json"), []byte(`{"$id": "https://example.com/note.json"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGen(root, nil); err == nil || !strings.Contains(err.Error(), "$id") {
		t.Errorf("gen with a misplaced $id: %v", err)
	}
}
