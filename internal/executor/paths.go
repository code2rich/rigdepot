// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package executor

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/code2rich/rigdepot/internal/brand"
)

// locator turns the paths of ext/adapter ("~/…" or absolute, with forward slashes) into a root
// and a path relative to it. It is lexical and takes the operating system as a parameter, so
// that the rules for every platform are tested on every platform.
type locator struct {
	goos string
	// roots are absolute paths with forward slashes, cleaned. roots[0] is the home directory.
	roots []string
}

func newLocator(goos string, home string, extra []string) locator {
	l := locator{goos: goos}
	for _, r := range append([]string{home}, extra...) {
		l.roots = append(l.roots, cleanPath(goos, strings.ReplaceAll(r, `\`, "/")))
	}
	return l
}

// cleanPath is path.Clean, except that it keeps the two slashes a Windows UNC path starts with.
func cleanPath(goos, p string) string {
	if goos == "windows" && strings.HasPrefix(p, "//") {
		return "/" + path.Clean(p[1:])
	}
	return path.Clean(p)
}

// checkSyntax rejects a path that is not written the way ext/adapter says.
func checkSyntax(p string) error {
	switch {
	case p == "":
		return errors.New("the path is empty")
	case strings.ContainsAny(p, "\\\x00"):
		return fmt.Errorf("%q contains a backslash or NUL; paths use forward slashes", p)
	}
	for _, el := range strings.Split(strings.TrimPrefix(p, "~/"), "/") {
		if el == ".." || el == "." {
			return fmt.Errorf("%q has a %q element", p, el)
		}
	}
	return nil
}

// isAbs reports whether p is absolute on goos.
func isAbs(goos, p string) bool {
	if goos == "windows" {
		// "C:/…", or a UNC path "//server/share/…".
		if len(p) >= 3 && p[1] == ':' && p[2] == '/' && ('a' <= p[0]|0x20 && p[0]|0x20 <= 'z') {
			return true
		}
		return strings.HasPrefix(p, "//")
	}
	return strings.HasPrefix(p, "/")
}

// within returns the part of p below root, or false when p is not below it. p equal to root
// gives "" and true.
func (l locator) within(root, p string) (string, bool) {
	if l.goos == "windows" {
		if len(p) >= len(root) && strings.EqualFold(p[:len(root)], root) {
			rest := p[len(root):]
			if rest == "" {
				return "", true
			}
			if rest[0] == '/' {
				return rest[1:], true
			}
			if strings.HasSuffix(root, "/") {
				return rest, true
			}
		}
		return "", false
	}
	if p == root {
		return "", true
	}
	if root == "/" {
		return p[1:], true
	}
	if rest, ok := strings.CutPrefix(p, root+"/"); ok {
		return rest, true
	}
	return "", false
}

// locate returns which root p is in and where below it, with forward slashes. "~" alone is the
// home directory itself.
func (l locator) locate(p string) (int, string, error) {
	if err := checkSyntax(p); err != nil {
		return 0, "", err
	}
	if p == "~" {
		return 0, "", nil
	}
	if rel, ok := strings.CutPrefix(p, "~/"); ok {
		if rel == "" || path.Clean(rel) != rel {
			return 0, "", fmt.Errorf("%q is not a clean path", p)
		}
		return 0, rel, nil
	}
	if !isAbs(l.goos, p) {
		return 0, "", fmt.Errorf("%q is neither inside the home directory (~/…) nor absolute", p)
	}
	if cleanPath(l.goos, p) != p {
		return 0, "", fmt.Errorf("%q is not a clean path", p)
	}
	// The most specific root wins: an extra root may lie inside another.
	best, bestRel, bestLen := -1, "", -1
	for i, r := range l.roots {
		if rel, ok := l.within(r, p); ok && len(r) > bestLen {
			best, bestRel, bestLen = i, rel, len(r)
		}
	}
	if best < 0 {
		return 0, "", fmt.Errorf("%q is outside the home directory and every configuration root", p)
	}
	return best, bestRel, nil
}

// under reports whether rel lies strictly below dir (both relative to the same root).
func (l locator) under(dir, rel string) bool {
	if dir == "" {
		return rel != ""
	}
	if l.goos == "windows" {
		return len(rel) > len(dir)+1 && rel[len(dir)] == '/' && strings.EqualFold(rel[:len(dir)], dir)
	}
	return strings.HasPrefix(rel, dir+"/")
}

// same reports whether two paths relative to the same root name the same place.
func (l locator) same(a, b string) bool {
	if l.goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// display returns how a target is written in the manifest: "~/…" inside the home directory and
// absolute otherwise.
func (l locator) display(root int, rel string) string {
	if root == 0 {
		if rel == "" {
			return "~"
		}
		return "~/" + rel
	}
	if rel == "" {
		return l.roots[root]
	}
	return strings.TrimSuffix(l.roots[root], "/") + "/" + rel
}

// prefixed reports whether the last element of a path carries the product's prefix and
// something after it.
func prefixed(p string) bool {
	name := path.Base(p)
	return len(name) > len(brand.Prefix) && strings.HasPrefix(name, brand.Prefix)
}

// checkTreeFile rejects a path inside a tree that could leave the tree or that some platform
// cannot create.
func checkTreeFile(p string) error {
	switch {
	case p == "":
		return errors.New("a file has an empty path")
	case strings.ContainsAny(p, "\\:\x00"):
		return fmt.Errorf("file %q contains a backslash, colon or NUL", p)
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("file %q is absolute", p)
	case path.Clean(p) != p:
		return fmt.Errorf("file %q is not a clean relative path", p)
	}
	for _, el := range strings.Split(p, "/") {
		if el == ".." || el == "." {
			return fmt.Errorf("file %q has a %q element", p, el)
		}
	}
	if p == MarkerFile {
		return fmt.Errorf("file %q is the marker the executor writes itself", p)
	}
	return nil
}

// keptPath returns where a target is copied below kept/<time>/: its path inside the home
// directory, or "_abs/" and its absolute path without the colon of a drive letter.
func keptPath(display string) string {
	if rel, ok := strings.CutPrefix(display, "~/"); ok {
		return filepath.FromSlash(rel)
	}
	return filepath.FromSlash("_abs/" + strings.TrimLeft(strings.ReplaceAll(display, ":", ""), "/"))
}
