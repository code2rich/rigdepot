// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adaptertest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/code2rich/rigdepot/internal/brand"
)

// node is one entry of a snapshot: a directory, or a file with its content.
type node struct {
	dir  bool
	data []byte
}

// snapshot is a directory tree, keyed by slash-separated paths relative to its root.
type snapshot map[string]node

// takeSnapshot reads the tree under root. The product's own local directory is left out: what
// the core keeps there (manifest, backups, kept copies) is not part of the person's files. A root
// that does not exist is an empty tree.
func takeSnapshot(root string) (snapshot, error) {
	s := snapshot{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root && os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == brand.Home {
			return filepath.SkipDir
		}
		switch {
		case d.IsDir():
			s[rel] = node{dir: true}
		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			s[rel] = node{data: data}
		default:
			return fmt.Errorf("%s: fixtures hold directories and regular files only", rel)
		}
		return nil
	})
	return s, err
}

// files returns the snapshot without its directories. Git does not keep empty directories, so
// a golden tree can only be compared by its files.
func (s snapshot) files() snapshot {
	out := snapshot{}
	for p, n := range s {
		if !n.dir {
			out[p] = n
		}
	}
	return out
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// change is one difference between two snapshots.
type change struct {
	path string
	what string // "+" created, "-" removed, "~" changed
	to   string // "dir", or the digest of the new content
}

func (c change) String() string { return c.what + " " + c.path + " " + c.to }

// diff lists what changed from a to b, sorted by path.
func diff(a, b snapshot) []change {
	var out []change
	for p, nb := range b {
		na, ok := a[p]
		switch {
		case !ok:
			out = append(out, change{p, "+", describeNode(nb)})
		case na.dir != nb.dir || !bytes.Equal(na.data, nb.data):
			out = append(out, change{p, "~", describeNode(nb)})
		}
	}
	for p := range a {
		if _, ok := b[p]; !ok {
			out = append(out, change{p, "-", ""})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func describeNode(n node) string {
	if n.dir {
		return "dir"
	}
	return digest(n.data)
}

func describeChanges(cs []change) string {
	if len(cs) == 0 {
		return "  (nothing)"
	}
	lines := make([]string, len(cs))
	for i, c := range cs {
		lines[i] = "  " + c.String()
	}
	return strings.Join(lines, "\n")
}

// copyTree copies the directory tree src into dst, which must not exist yet. A src that does not
// exist gives an empty dst.
func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == src && os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: fixtures hold directories and regular files only", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// writeSnapshot replaces the tree under root by the files of s.
func writeSnapshot(root string, s snapshot) error {
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	for p, n := range s {
		if n.dir {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, n.data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
