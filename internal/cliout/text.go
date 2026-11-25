// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cliout

import (
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/width"
)

// DisplayWidth returns how many terminal columns s takes: two for each wide
// or full-width character (a Chinese character), one for the others. Tables
// are aligned by it, not by bytes or runes.
func DisplayWidth(s string) int {
	n := 0
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		switch width.LookupRune(r).Kind() {
		case width.EastAsianWide, width.EastAsianFullwidth:
			n += 2
		default:
			n++
		}
	}
	return n
}

// PadRight pads s with spaces to the given display width.
func PadRight(s string, w int) string {
	if d := DisplayWidth(s); d < w {
		return s + strings.Repeat(" ", w-d)
	}
	return s
}

// ShowPath shortens a path below the home directory for people: ~/… on
// macOS and Linux, %USERPROFILE%\… on Windows. Other paths are unchanged.
func ShowPath(path, home, goos string) string {
	sep := "/"
	prefix := "~"
	if goos == "windows" {
		sep, prefix = `\`, "%USERPROFILE%"
	}
	if home == "" {
		return path
	}
	clean := func(p string) string {
		if goos == "windows" {
			return strings.TrimRight(strings.ReplaceAll(p, "/", `\`), `\`)
		}
		return strings.TrimRight(filepath.ToSlash(p), "/")
	}
	p, h := clean(path), clean(home)
	same := func(a, b string) bool {
		if goos == "windows" {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	if same(p, h) {
		return prefix
	}
	if len(p) > len(h) && same(p[:len(h)], h) && p[len(h):len(h)+1] == sep {
		return prefix + p[len(h):]
	}
	return path
}
