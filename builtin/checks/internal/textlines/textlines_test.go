// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package textlines

import (
	"fmt"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/ext/check/checktest"
)

func TestMain(m *testing.M) { checktest.Main(m) }

func TestEach(t *testing.T) {
	files := fstest.MapFS{
		"SKILL.md":       {Data: []byte("\xef\xbb\xbfone\r\ntwo\r\n")},
		"b/no-newline":   {Data: []byte("a\n\nb")},
		"empty":          {Data: nil},
		"image.png":      {Data: []byte("\x89PNG\x00\x00secret")},
		"scripts/run.sh": {Data: []byte("echo\n")},
	}
	var got []string
	err := Each(files, func(file string, line int, text string) {
		got = append(got, fmt.Sprintf("%s:%d:%s", file, line, text))
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"SKILL.md:1:one", "SKILL.md:2:two", "b/no-newline:1:a", "b/no-newline:2:", "b/no-newline:3:b", "scripts/run.sh:1:echo"}
	if !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}
