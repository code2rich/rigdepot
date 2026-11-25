// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package bundle

import (
	"errors"
	"testing"
	"testing/fstest"
)

func TestDigestContentsEqualsDigest(t *testing.T) {
	files := map[string][]byte{
		"SKILL.md":         []byte("---\nname: x\n---\n"),
		"a-b":              []byte("1"),
		"a/b":              []byte("2"),
		"scripts/run.sh":   []byte("#!/bin/sh\n"),
		MarkerFile:         []byte(`{"marker":true}`),
		"nested/deep/f.md": nil,
	}
	fsys := fstest.MapFS{}
	for p, b := range files {
		fsys[p] = &fstest.MapFile{Data: b}
	}
	want, err := Digest(fsys)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DigestContents(files)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("DigestContents = %s, Digest = %s", got, want)
	}
	if _, err := DigestContents(map[string][]byte{MarkerFile: nil}); !errors.Is(err, ErrEmpty) {
		t.Fatalf("only the marker: err = %v, want ErrEmpty", err)
	}
}
