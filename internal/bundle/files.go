// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package bundle

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
)

// Files is a read-only file system held in memory. Snapshot, SnapshotFile and Unpack return
// one that has passed the path safety checks and holds regular files only; ReadTree returns one
// that may also hold symbolic links, hard links and special files, so that the checks can be
// applied resource by resource later.
//
// Opening anything that is not a regular file or a directory fails; the entry is still listed
// by ReadDir with its type, which is how a check sees it. Files is safe for concurrent use.
type Files struct {
	entries map[string]*entry // by path; directories included, the root is "."
}

type entry struct {
	name     string
	mode     fs.FileMode // type bits, plus 0755 or 0644 for files
	data     []byte
	sys      any      // the tar header for entries that came from a tar
	children []string // directories: sorted names
}

func newFiles() *Files {
	return &Files{entries: map[string]*entry{".": {name: ".", mode: fs.ModeDir | 0o755}}}
}

// add puts an entry at a path that fs.ValidPath accepts, creating its parent directories. It
// fails when the path or one of its parents is already there as something else.
func (f *Files) add(p string, e *entry) error {
	if _, dup := f.entries[p]; dup {
		if e.mode.IsDir() && f.entries[p].mode.IsDir() {
			return nil
		}
		return errors.New("appears twice")
	}
	if err := f.mkdirs(path.Dir(p)); err != nil {
		return err
	}
	e.name = path.Base(p)
	f.entries[p] = e
	parent := f.entries[path.Dir(p)]
	i, _ := slices.BinarySearch(parent.children, e.name)
	parent.children = slices.Insert(parent.children, i, e.name)
	return nil
}

func (f *Files) mkdirs(dir string) error {
	if e, ok := f.entries[dir]; ok {
		if !e.mode.IsDir() {
			return errors.New("is inside " + dir + ", which is not a directory")
		}
		return nil
	}
	return f.add(dir, &entry{mode: fs.ModeDir | 0o755})
}

// Paths returns the paths of the regular files, sorted in byte order.
func (f *Files) Paths() []string {
	var out []string
	for p, e := range f.entries {
		if e.mode.IsRegular() {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

func (f *Files) lookup(op, name string) (*entry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: op, Path: name, Err: fs.ErrInvalid}
	}
	e, ok := f.entries[name]
	if !ok {
		return nil, &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
	}
	return e, nil
}

// Open implements fs.FS.
func (f *Files) Open(name string) (fs.File, error) {
	e, err := f.lookup("open", name)
	if err != nil {
		return nil, err
	}
	switch {
	case e.mode.IsDir():
		return &openDir{f: f, path: name, e: e}, nil
	case e.mode.IsRegular():
		return &openFile{e: e, r: bytes.NewReader(e.data)}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: errors.New("not a regular file or directory")}
}

// ReadFile implements fs.ReadFileFS. The slice is the caller's own.
func (f *Files) ReadFile(name string) ([]byte, error) {
	e, err := f.lookup("read", name)
	if err != nil {
		return nil, err
	}
	if !e.mode.IsRegular() {
		return nil, &fs.PathError{Op: "read", Path: name, Err: errors.New("not a regular file")}
	}
	return bytes.Clone(e.data), nil
}

// ReadDir implements fs.ReadDirFS: the entries of a directory sorted by name.
func (f *Files) ReadDir(name string) ([]fs.DirEntry, error) {
	e, err := f.lookup("readdir", name)
	if err != nil {
		return nil, err
	}
	if !e.mode.IsDir() {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: errors.New("not a directory")}
	}
	out := make([]fs.DirEntry, 0, len(e.children))
	for _, c := range e.children {
		out = append(out, fs.FileInfoToDirEntry(info{f.entries[path.Join(name, c)]}))
	}
	return out, nil
}

// Stat implements fs.StatFS. It does not follow links.
func (f *Files) Stat(name string) (fs.FileInfo, error) {
	e, err := f.lookup("stat", name)
	if err != nil {
		return nil, err
	}
	return info{e}, nil
}

// Sub implements fs.SubFS.
func (f *Files) Sub(dir string) (fs.FS, error) {
	e, err := f.lookup("sub", dir)
	if err != nil {
		return nil, err
	}
	if !e.mode.IsDir() {
		return nil, &fs.PathError{Op: "sub", Path: dir, Err: errors.New("not a directory")}
	}
	if dir == "." {
		return f, nil
	}
	sub := newFiles()
	prefix := dir + "/"
	for p, e := range f.entries {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			c := *e
			c.children = slices.Clone(e.children)
			sub.entries[rest] = &c
		}
	}
	sub.entries["."].children = slices.Clone(e.children)
	return sub, nil
}

type info struct{ e *entry }

func (i info) Name() string       { return i.e.name }
func (i info) Size() int64        { return int64(len(i.e.data)) }
func (i info) Mode() fs.FileMode  { return i.e.mode }
func (i info) ModTime() time.Time { return time.Unix(0, 0) }
func (i info) IsDir() bool        { return i.e.mode.IsDir() }
func (i info) Sys() any           { return i.e.sys }

type openFile struct {
	e *entry
	r *bytes.Reader
}

func (o *openFile) Stat() (fs.FileInfo, error) { return info{o.e}, nil }
func (o *openFile) Read(b []byte) (int, error) { return o.r.Read(b) }
func (o *openFile) Close() error               { return nil }

type openDir struct {
	f    *Files
	path string
	e    *entry
	pos  int
}

func (o *openDir) Stat() (fs.FileInfo, error) { return info{o.e}, nil }
func (o *openDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: o.path, Err: errors.New("is a directory")}
}
func (o *openDir) Close() error { return nil }

func (o *openDir) ReadDir(n int) ([]fs.DirEntry, error) {
	all, _ := o.f.ReadDir(o.path)
	rest := all[o.pos:]
	if n <= 0 {
		o.pos = len(all)
		return rest, nil
	}
	if len(rest) == 0 {
		return nil, io.EOF
	}
	rest = rest[:min(n, len(rest))]
	o.pos += len(rest)
	return rest, nil
}

var (
	_ fs.ReadFileFS = (*Files)(nil)
	_ fs.ReadDirFS  = (*Files)(nil)
	_ fs.StatFS     = (*Files)(nil)
	_ fs.SubFS      = (*Files)(nil)
)
