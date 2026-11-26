// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A real release pushes one multi-architecture image; goreleaser's dockers_v2 records it by name,
// with or without the digest, and no per-architecture image. verifyDist must accept that.
func TestPushedImageListed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifacts.json")
	ref := "ghcr.io/example/server:0.1.0"
	for _, tc := range []struct {
		json string
		ok   bool
	}{
		{`[{"name":"ghcr.io/example/server:0.1.0@sha256:c02d","type":"Docker Image V2"}]`, true},
		{`[{"name":"ghcr.io/example/server:0.1.0","type":"Published Docker Image"}]`, true},
		{`[{"name":"ghcr.io/example/server:latest@sha256:c02d","type":"Docker Image V2"}]`, false},
		{`[{"name":"ghcr.io/example/server:0.1.0-rc.1","type":"Docker Image V2"}]`, false},
		{`[]`, false},
	} {
		if err := os.WriteFile(path, []byte(tc.json), 0o644); err != nil {
			t.Fatal(err)
		}
		err := pushedImageListed(path, ref)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok %v", tc.json, err, tc.ok)
		}
		if err != nil && !strings.Contains(err.Error(), ref) {
			t.Errorf("the error should name the image: %v", err)
		}
	}
}
