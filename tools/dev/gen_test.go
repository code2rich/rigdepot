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
			if _, err := generate(writeBrand(t, content)); err == nil {
				t.Error("generate succeeded")
			}
		})
	}
	if _, err := generate(t.TempDir()); err == nil {
		t.Error("generate succeeded without a brand file")
	}
}
