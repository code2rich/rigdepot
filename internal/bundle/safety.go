// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package bundle

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// The limits of one resource. The file count and total size are product rules; the path limits
// leave room for the prefix in front of a path on Windows, where a path has at most 260
// characters.
const (
	MaxFiles     = 200
	MaxBytes     = 5 << 20
	MaxPathBytes = 180
	MaxDepth     = 8
)

// Rule names one of the path safety rules. The values are stable: they appear in error output
// and are the keys of their messages.
type Rule string

const (
	// RuleNotRelative: a path is absolute, has a ".." or "." component or an empty one,
	// contains a backslash, or starts with a drive letter. It could land outside the target.
	RuleNotRelative Rule = "not-relative"
	// RuleLink: a symbolic link or a hard link. It could point outside the target, and the
	// platforms treat links differently.
	RuleLink Rule = "link"
	// RuleSpecial: a device, a pipe, a socket or anything else that is neither a regular file
	// nor a directory.
	RuleSpecial Rule = "special"
	// RuleWindowsName: a name Windows cannot create: a reserved device name, a trailing dot or
	// space, one of < > : " | ? *, or a control character.
	RuleWindowsName Rule = "windows-name"
	// RuleCaseCollision: two names in one directory differ only in case. The default file
	// systems of macOS and Windows would make them one.
	RuleCaseCollision Rule = "case-collision"
	// RuleTooLong: a path is longer than MaxPathBytes bytes.
	RuleTooLong Rule = "too-long"
	// RuleTooDeep: a path has more than MaxDepth components.
	RuleTooDeep Rule = "too-deep"
	// RuleTooManyFiles: the resource has more than MaxFiles files.
	RuleTooManyFiles Rule = "too-many-files"
	// RuleTooLarge: the files of the resource add up to more than MaxBytes bytes.
	RuleTooLarge Rule = "too-large"
)

// Problem is one broken path safety rule.
type Problem struct {
	// Path is relative to the resource's root, as found. Empty for the limits on the whole
	// resource.
	Path   string
	Rule   Rule
	Detail string
}

func (p Problem) String() string {
	if p.Path == "" {
		return fmt.Sprintf("%s: %s", p.Rule, p.Detail)
	}
	return fmt.Sprintf("%s: %q: %s", p.Rule, p.Path, p.Detail)
}

// UnsafeError lists every path safety rule a set of files breaks. Any one of them is enough to
// refuse the resource.
type UnsafeError struct {
	Problems []Problem
}

func (e *UnsafeError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.String()
	}
	return "unsafe paths: " + strings.Join(parts, "; ")
}

// Has reports whether one of the problems breaks rule r.
func (e *UnsafeError) Has(r Rule) bool {
	return slices.ContainsFunc(e.Problems, func(p Problem) bool { return p.Rule == r })
}

// checker collects the problems of one set of files.
type checker struct {
	problems []Problem
	files    int
	bytes    int64
	folded   map[string]string // lower-case path -> path as found
}

func (c *checker) add(p string, r Rule, format string, args ...any) {
	c.problems = append(c.problems, Problem{Path: p, Rule: r, Detail: fmt.Sprintf(format, args...)})
}

// reserved are the device names Windows reserves in every directory, with any extension.
var reserved = func() map[string]bool {
	m := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true}
	for _, d := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³"} {
		m["COM"+d] = true
		m["LPT"+d] = true
	}
	return m
}()

// name checks one path as found, slash-separated and relative to the resource's root. It
// returns false when the path cannot be placed at all.
func (c *checker) name(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		c.add(p, RuleNotRelative, "only relative paths with forward slashes are allowed")
		return false
	}
	parts := strings.Split(p, "/")
	if len(parts[0]) >= 2 && parts[0][1] == ':' && isLetter(parts[0][0]) {
		c.add(p, RuleNotRelative, "starts with a drive letter")
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			c.add(p, RuleNotRelative, "has an empty, \".\" or \"..\" component")
			return false
		}
	}
	for _, part := range parts {
		if why := windowsName(part); why != "" {
			c.add(p, RuleWindowsName, "%q %s", part, why)
		}
	}
	if len(p) > MaxPathBytes {
		c.add(p, RuleTooLong, "%d bytes, at most %d", len(p), MaxPathBytes)
	}
	if len(parts) > MaxDepth {
		c.add(p, RuleTooDeep, "%d levels, at most %d", len(parts), MaxDepth)
	}
	// Every prefix of the path is a name in some directory; two that fold to the same text
	// would become one on a file system that ignores case.
	if c.folded == nil {
		c.folded = map[string]string{}
	}
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		key := strings.ToLower(prefix)
		if seen, ok := c.folded[key]; ok && seen != prefix {
			c.add(prefix, RuleCaseCollision, "differs from %q only in case", seen)
			continue
		}
		c.folded[key] = prefix
	}
	return true
}

func isLetter(b byte) bool { return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' }

// windowsName says why Windows cannot create a file or directory with this name, or "".
func windowsName(name string) string {
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return "ends with a dot or a space"
	}
	for _, r := range name {
		if r < 0x20 {
			return "contains a control character"
		}
		if strings.ContainsRune(`<>:"|?*`, r) {
			return fmt.Sprintf("contains %q", r)
		}
	}
	stem, _, _ := strings.Cut(name, ".")
	if reserved[strings.ToUpper(strings.TrimRight(stem, " "))] {
		return "is a reserved device name"
	}
	return ""
}

// kind checks the type of an entry and reports whether it is a regular file.
func (c *checker) kind(p string, fi fs.FileInfo) bool {
	m := fi.Mode()
	switch {
	case m&fs.ModeSymlink != 0:
		c.add(p, RuleLink, "is a symbolic link")
	case isHardLink(fi):
		c.add(p, RuleLink, "is a hard link")
	case m.IsRegular():
		return true
	case m.IsDir():
	default:
		c.add(p, RuleSpecial, "is not a regular file or directory (%s)", m.Type())
	}
	return false
}

// isHardLink reports whether fi describes a hard link: an entry of that type in a tar, or a file
// on disk with more than one name.
func isHardLink(fi fs.FileInfo) bool {
	if h, ok := fi.Sys().(*tar.Header); ok {
		return h.Typeflag == tar.TypeLink
	}
	return linkCount(fi) > 1
}

// file counts a regular file towards the limits. It returns false once the total size is over.
func (c *checker) file(size int64) bool {
	c.files++
	c.bytes += size
	return c.bytes <= MaxBytes
}

func (c *checker) result() error {
	if c.files > MaxFiles {
		c.add("", RuleTooManyFiles, "%d files, at most %d", c.files, MaxFiles)
	}
	if c.bytes > MaxBytes {
		c.add("", RuleTooLarge, "%d bytes or more, at most %d", c.bytes, MaxBytes)
	}
	if len(c.problems) == 0 {
		return nil
	}
	return &UnsafeError{Problems: c.problems}
}

// Snapshot reads every file below the root of fsys into memory, applying the path safety
// checks. It returns an *UnsafeError listing every broken rule, or any error reading fsys.
//
// Entries are taken as fsys reports them and links are not followed: for os.DirFS a symbolic
// link is refused, not read through.
func Snapshot(fsys fs.FS) (*Files, error) {
	var c checker
	out := newFiles()
	over := false
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if !c.name(p) {
			return skipDir(d)
		}
		if !c.kind(p, fi) {
			if d.IsDir() {
				return nil
			}
			return skipDir(d)
		}
		if !c.file(fi.Size()) || over {
			over = true
			return nil
		}
		data, err := readLimited(fsys, p)
		if err != nil {
			return err
		}
		c.bytes += int64(len(data)) - fi.Size() // the size the file really had
		return out.add(p, &entry{mode: fileMode(fi.Mode()), data: data})
	})
	if err != nil {
		return nil, err
	}
	if err := c.result(); err != nil {
		return nil, err
	}
	return out, nil
}

func skipDir(d fs.DirEntry) error {
	if d.IsDir() {
		return fs.SkipDir
	}
	return nil
}

// readLimited reads a file, but never more than MaxBytes+1 bytes of it.
func readLimited(fsys fs.FS, p string) ([]byte, error) {
	f, err := fsys.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, MaxBytes+1))
}

// fileMode keeps of a file's mode only whether it can be executed.
func fileMode(m fs.FileMode) fs.FileMode {
	if m&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// SnapshotFile reads the one file name of fsys into memory as a file system that holds only
// it, under its base name. This is the layout of a single-file resource. The same checks as
// Snapshot apply, to the base name.
func SnapshotFile(fsys fs.FS, name string) (*Files, error) {
	dir, base := path.Split(name)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "."
	}
	// The directory listing describes the entry itself, without following a link.
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(entries, func(e fs.DirEntry) bool { return e.Name() == base })
	if i < 0 {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	fi, err := entries[i].Info()
	if err != nil {
		return nil, err
	}
	var c checker
	out := newFiles()
	if c.name(base) && c.kind(base, fi) && c.file(fi.Size()) {
		data, err := readLimited(fsys, name)
		if err != nil {
			return nil, err
		}
		c.bytes = int64(len(data))
		if err := out.add(base, &entry{mode: fileMode(fi.Mode()), data: data}); err != nil {
			return nil, err
		}
	}
	if err := c.result(); err != nil {
		return nil, err
	}
	return out, nil
}
