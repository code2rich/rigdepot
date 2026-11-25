// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package gitrepo

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
)

// tree is a read-only file system held in memory: the content of one commit.
type tree struct {
	entries map[string]*entry // by path; "." is the root
}

type entry struct {
	name     string
	mode     fs.FileMode
	data     []byte
	modTime  time.Time
	children []*entry // directories only, sorted by name
}

// readTar builds a tree from the tar stream git archive writes. Symbolic links are not files of
// the tree; they are returned separately, by path, with their targets.
func readTar(r io.Reader) (*tree, map[string]string, error) {
	t := &tree{entries: map[string]*entry{".": {name: ".", mode: fs.ModeDir | 0o555}}}
	links := map[string]string{}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading the archive: %w", err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := strings.TrimSuffix(h.Name, "/")
		if !fs.ValidPath(name) || name == "." {
			return nil, nil, fmt.Errorf("the archive contains an invalid path %q", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if _, err := t.dir(name, h.ModTime); err != nil {
				return nil, nil, err
			}
		case tar.TypeReg:
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, nil, fmt.Errorf("reading the archive: %w", err)
			}
			parent, err := t.dir(path.Dir(name), h.ModTime)
			if err != nil {
				return nil, nil, err
			}
			if _, dup := t.entries[name]; dup {
				return nil, nil, fmt.Errorf("the archive contains %q twice", name)
			}
			mode := fs.FileMode(0o444)
			if h.Mode&0o111 != 0 {
				mode = 0o555
			}
			e := &entry{name: path.Base(name), mode: mode, data: data, modTime: h.ModTime}
			t.entries[name] = e
			parent.children = append(parent.children, e)
		case tar.TypeSymlink:
			if _, err := t.dir(path.Dir(name), h.ModTime); err != nil {
				return nil, nil, err
			}
			links[name] = h.Linkname
		default:
			return nil, nil, fmt.Errorf("the archive contains %q of an unexpected type", name)
		}
	}
	for _, e := range t.entries {
		slices.SortFunc(e.children, func(a, b *entry) int { return strings.Compare(a.name, b.name) })
	}
	return t, links, nil
}

// dir returns the directory at name, creating it and its parents as needed.
func (t *tree) dir(name string, mod time.Time) (*entry, error) {
	if e, ok := t.entries[name]; ok {
		if !e.mode.IsDir() {
			return nil, fmt.Errorf("the archive contains %q both as a file and as a directory", name)
		}
		return e, nil
	}
	parent, err := t.dir(path.Dir(name), mod)
	if err != nil {
		return nil, err
	}
	e := &entry{name: path.Base(name), mode: fs.ModeDir | 0o555, modTime: mod}
	t.entries[name] = e
	parent.children = append(parent.children, e)
	return e, nil
}

func (t *tree) lookup(op, name string) (*entry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: op, Path: name, Err: fs.ErrInvalid}
	}
	e, ok := t.entries[name]
	if !ok {
		return nil, &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
	}
	return e, nil
}

// Open implements fs.FS.
func (t *tree) Open(name string) (fs.File, error) {
	e, err := t.lookup("open", name)
	if err != nil {
		return nil, err
	}
	if e.mode.IsDir() {
		return &openDir{entry: e}, nil
	}
	return &openFile{entry: e, r: bytes.NewReader(e.data)}, nil
}

// ReadFile implements fs.ReadFileFS. The caller gets a copy.
func (t *tree) ReadFile(name string) ([]byte, error) {
	e, err := t.lookup("read", name)
	if err != nil {
		return nil, err
	}
	if e.mode.IsDir() {
		return nil, &fs.PathError{Op: "read", Path: name, Err: errors.New("is a directory")}
	}
	return slices.Clone(e.data), nil
}

// ReadDir implements fs.ReadDirFS.
func (t *tree) ReadDir(name string) ([]fs.DirEntry, error) {
	e, err := t.lookup("readdir", name)
	if err != nil {
		return nil, err
	}
	if !e.mode.IsDir() {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: errors.New("not a directory")}
	}
	return dirEntries(e.children), nil
}

// Stat implements fs.StatFS.
func (t *tree) Stat(name string) (fs.FileInfo, error) {
	e, err := t.lookup("stat", name)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func dirEntries(children []*entry) []fs.DirEntry {
	out := make([]fs.DirEntry, len(children))
	for i, c := range children {
		out[i] = fs.FileInfoToDirEntry(c)
	}
	return out
}

// entry is its own fs.FileInfo.
func (e *entry) Name() string       { return e.name }
func (e *entry) Size() int64        { return int64(len(e.data)) }
func (e *entry) Mode() fs.FileMode  { return e.mode }
func (e *entry) ModTime() time.Time { return e.modTime }
func (e *entry) IsDir() bool        { return e.mode.IsDir() }
func (e *entry) Sys() any           { return nil }

type openFile struct {
	*entry
	r *bytes.Reader
}

func (f *openFile) Stat() (fs.FileInfo, error) { return f.entry, nil }
func (f *openFile) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *openFile) Seek(off int64, whence int) (int64, error) {
	return f.r.Seek(off, whence)
}
func (f *openFile) ReadAt(p []byte, off int64) (int, error) { return f.r.ReadAt(p, off) }
func (f *openFile) Close() error                            { return nil }

type openDir struct {
	*entry
	offset int
}

func (d *openDir) Stat() (fs.FileInfo, error) { return d.entry, nil }
func (d *openDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.name, Err: errors.New("is a directory")}
}
func (d *openDir) Close() error { return nil }

// ReadDir implements fs.ReadDirFile.
func (d *openDir) ReadDir(n int) ([]fs.DirEntry, error) {
	rest := d.children[d.offset:]
	if n <= 0 {
		d.offset = len(d.children)
		return dirEntries(rest), nil
	}
	if len(rest) == 0 {
		return nil, io.EOF
	}
	if n > len(rest) {
		n = len(rest)
	}
	d.offset += n
	return dirEntries(rest[:n]), nil
}
