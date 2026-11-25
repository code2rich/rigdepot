// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package e2e

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// node is one entry of a directory tree: its type and permission bits, and a file's bytes.
type node struct {
	mode fs.FileMode
	data []byte
}

// tree is a directory tree by slash-separated path relative to its root.
type tree map[string]node

// snapshot reads the whole tree under root. Symbolic links are recorded as such and not followed.
func snapshot(t *testing.T, root string) tree {
	t.Helper()
	out := tree{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		n := node{mode: info.Mode().Type() | info.Mode().Perm()}
		// Windows has no Unix permission bits worth comparing.
		if runtime.GOOS == "windows" {
			n.mode = info.Mode().Type()
		}
		switch {
		case info.Mode().IsRegular():
			if n.data, err = os.ReadFile(p); err != nil {
				return err
			}
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			n.data = []byte(target)
		}
		out[filepath.ToSlash(rel)] = n
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// without returns the tree without dir and everything under it.
func (tr tree) without(dir string) tree {
	out := tree{}
	for p, n := range tr {
		if p != dir && !strings.HasPrefix(p, dir+"/") {
			out[p] = n
		}
	}
	return out
}

// under returns dir and everything under it.
func (tr tree) under(dir string) tree {
	out := tree{}
	for p, n := range tr {
		if p == dir || strings.HasPrefix(p, dir+"/") {
			out[p] = n
		}
	}
	return out
}

// diffTrees describes every difference between two trees, one line per path, or returns "".
func diffTrees(want, got tree) string {
	paths := map[string]bool{}
	for p := range want {
		paths[p] = true
	}
	for p := range got {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	var b strings.Builder
	for _, p := range sorted {
		w, inWant := want[p]
		g, inGot := got[p]
		switch {
		case !inGot:
			fmt.Fprintf(&b, "  - %s: gone\n", p)
		case !inWant:
			fmt.Fprintf(&b, "  + %s: new\n", p)
		case w.mode != g.mode:
			fmt.Fprintf(&b, "  ~ %s: mode %v, was %v\n", p, g.mode, w.mode)
		case !bytes.Equal(w.data, g.data):
			fmt.Fprintf(&b, "  ~ %s: content differs %s\n", p, firstDifference(w.data, g.data))
		}
	}
	return b.String()
}

// firstDifference shows where two byte strings first differ, with a little context.
func firstDifference(a, b []byte) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := max(0, i-20)
	return fmt.Sprintf("at byte %d (was %d bytes, now %d): was %q, now %q", i, len(a), len(b), a[from:min(len(a), i+20)], b[from:min(len(b), i+20)])
}
