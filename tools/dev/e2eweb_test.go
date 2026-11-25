// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestParseE2EArgs(t *testing.T) {
	root := t.TempDir()
	o, err := parseE2EArgs(root, nil)
	if err != nil || o.only != "" || o.shots || o.out != filepath.Join(root, ".dev", "e2e-web") || o.extra != nil {
		t.Fatalf("defaults: %+v, %v", o, err)
	}
	out := filepath.Join(root, "out")
	o, err = parseE2EArgs(root, []string{"real", "--shots", "--out", out, "--", "--project", "webkit", "--shots"})
	if err != nil || o.only != "real" || !o.shots || o.out != out || !slices.Equal(o.extra, []string{"--project", "webkit", "--shots"}) {
		t.Fatalf("parsed: %+v, %v", o, err)
	}
	if o, err := parseE2EArgs(root, []string{"--out=" + out, "mock"}); err != nil || o.out != out || o.only != "mock" {
		t.Fatalf("--out=: %+v, %v", o, err)
	}
	for _, bad := range [][]string{{"both"}, {"--out"}, {"--headed"}} {
		if _, err := parseE2EArgs(root, bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestFreePort(t *testing.T) {
	a, err := freePort()
	if err != nil || a == "" || a == "0" {
		t.Fatalf("freePort: %q, %v", a, err)
	}
}
