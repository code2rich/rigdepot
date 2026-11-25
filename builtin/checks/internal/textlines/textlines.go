// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package textlines walks the lines of a resource's text files, for the check rules that look
// for patterns in text.
package textlines

import (
	"bytes"
	"io/fs"
	"strings"
)

// sniff is how much of a file is looked at to decide whether it is binary.
const sniff = 8000

// Each calls fn for every line of every text file in files, in path order, with lines counted
// from 1. The line has no line ending; a byte order mark at the start of a file is removed.
// Binary files, recognised by a NUL byte near the start, are skipped. An error reading files is
// returned as it is: a rule that could not read a file has not looked at it.
func Each(files fs.FS, fn func(file string, line int, text string)) error {
	return fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(files, p)
		if err != nil {
			return err
		}
		if bytes.IndexByte(data[:min(len(data), sniff)], 0) >= 0 {
			return nil
		}
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		for i, l := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if len(data) == 0 {
				break
			}
			fn(p, i+1, strings.TrimSuffix(l, "\r"))
		}
		return nil
	})
}
