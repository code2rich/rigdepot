// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package check

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
)

// memFS is a read-only file system over a resource's files in memory. It is all a rule gets to
// read: there is no way from it to the disk.
type memFS struct {
	files map[string][]byte
	dirs  map[string][]string // directory -> names of its entries, sorted
}

func newMemFS(files map[string][]byte) (*memFS, error) {
	m := &memFS{files: make(map[string][]byte, len(files)), dirs: map[string][]string{".": nil}}
	for name, data := range files {
		if !fs.ValidPath(name) || name == "." || strings.Contains(name, `\`) {
			return nil, fmt.Errorf("%q is not a relative path with forward slashes: %w", name, fs.ErrInvalid)
		}
		m.files[name] = data
	}
	for name := range m.files {
		for child := name; child != "."; child = path.Dir(child) {
			parent := path.Dir(child)
			if _, isFile := m.files[parent]; isFile {
				return nil, fmt.Errorf("%q is both a file and a directory: %w", parent, fs.ErrInvalid)
			}
			if !slices.Contains(m.dirs[parent], path.Base(child)) {
				m.dirs[parent] = append(m.dirs[parent], path.Base(child))
			}
		}
	}
	for dir := range m.dirs {
		slices.Sort(m.dirs[dir])
	}
	return m, nil
}

func (m *memFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if data, ok := m.files[name]; ok {
		return &memFile{info: memInfo{name: path.Base(name), size: int64(len(data))}, r: bytes.NewReader(data)}, nil
	}
	if _, ok := m.dirs[name]; ok {
		return &memDir{fsys: m, name: name}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// ReadFile returns a copy, so that a rule cannot change what the next rule reads.
func (m *memFS) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrInvalid}
	}
	data, ok := m.files[name]
	if !ok {
		if _, isDir := m.dirs[name]; isDir {
			return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrInvalid}
		}
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrNotExist}
	}
	return bytes.Clone(data), nil
}

func (m *memFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	names, ok := m.dirs[name]
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	out := make([]fs.DirEntry, 0, len(names))
	for _, n := range names {
		out = append(out, fs.FileInfoToDirEntry(m.stat(path.Join(name, n))))
	}
	return out, nil
}

func (m *memFS) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	if info := m.stat(name); info != nil {
		return info, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

func (m *memFS) stat(name string) fs.FileInfo {
	if data, ok := m.files[name]; ok {
		return memInfo{name: path.Base(name), size: int64(len(data))}
	}
	if _, ok := m.dirs[name]; ok {
		return memInfo{name: path.Base(name), dir: true}
	}
	return nil
}

type memInfo struct {
	name string
	size int64
	dir  bool
}

func (i memInfo) Name() string { return i.name }
func (i memInfo) Size() int64  { return i.size }
func (i memInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return i.dir }
func (i memInfo) Sys() any           { return nil }

type memFile struct {
	info memInfo
	r    *bytes.Reader
}

func (f *memFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *memFile) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *memFile) Close() error               { return nil }

type memDir struct {
	fsys *memFS
	name string
	read int // entries already returned by ReadDir
}

func (d *memDir) Stat() (fs.FileInfo, error) { return d.fsys.stat(d.name), nil }
func (d *memDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.name, Err: fs.ErrInvalid}
}
func (d *memDir) Close() error { return nil }

func (d *memDir) ReadDir(n int) ([]fs.DirEntry, error) {
	all, _ := d.fsys.ReadDir(d.name)
	rest := all[d.read:]
	if n <= 0 {
		d.read = len(all)
		return rest, nil
	}
	if len(rest) == 0 {
		return nil, io.EOF
	}
	if n > len(rest) {
		n = len(rest)
	}
	d.read += n
	return rest[:n], nil
}
