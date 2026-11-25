// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/code2rich/rigdepot/internal/bundle"
)

// An upload is turned into a repository of one resource and read by the same reader as an
// import (repoformat.Read on bundle.ReadTree), so that the path safety rules, the schema and the
// checks are exactly an import's. This file reads the three archive formats into entries and
// writes them out as a tar the reader takes.

// entry is one member of an uploaded archive.
type entry struct {
	name string      // slash-separated, relative, without a trailing slash
	typ  byte        // tar.TypeReg, TypeDir, TypeSymlink, TypeLink or another (refused later)
	mode fs.FileMode // permission bits of a file
	data []byte
	link string
}

// maxEntries bounds the members of one upload, directories included; bundle.MaxFiles bounds
// the files.
const maxEntries = 4 * bundle.MaxFiles

// archiveFormat tells the three accepted formats apart by their first bytes.
func archiveFormat(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")), bytes.HasPrefix(data, []byte("PK\x05\x06")):
		return "zip"
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		return "tar.gz"
	case len(data) >= 262 && string(data[257:262]) == "ustar":
		return "tar"
	}
	return ""
}

// readArchive reads an uploaded archive into its members. File contents are bounded by
// bundle.MaxBytes in all, so that a small compressed upload cannot unpack into something
// large; anything over is refused as too large.
func readArchive(data []byte) ([]entry, error) {
	switch archiveFormat(data) {
	case "zip":
		return readZip(data)
	case "tar.gz":
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, ErrUnknownFormat
		}
		return readTar(zr)
	case "tar":
		return readTar(bytes.NewReader(data))
	}
	return nil, ErrUnknownFormat
}

// budget counts what an archive unpacks into.
type budget struct{ bytes, entries int }

func (b *budget) take(n int) error {
	b.bytes += n
	b.entries++
	if b.bytes > bundle.MaxBytes {
		return unsafe("", string(bundle.RuleTooLarge), fmt.Sprintf("more than %d bytes", bundle.MaxBytes))
	}
	if b.entries > maxEntries {
		return unsafe("", string(bundle.RuleTooManyFiles), fmt.Sprintf("more than %d entries", maxEntries))
	}
	return nil
}

// readAll reads at most what is left of the budget, plus one byte to notice more.
func (b *budget) readAll(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(bundle.MaxBytes-b.bytes+1)))
	if err != nil {
		return nil, err
	}
	return data, b.take(len(data))
}

func readTar(r io.Reader) ([]entry, error) {
	tr := tar.NewReader(r)
	var out []entry
	var b budget
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, &Error{Kind: KindInvalid, Code: ErrUnknownFormat.Code, Fields: []FieldError{{Field: "body", Code: "corrupt", Detail: err.Error()}}}
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name, err := entryName(h.Name)
		if err != nil {
			return nil, err
		}
		if name == "" {
			continue
		}
		e := entry{name: name, typ: h.Typeflag, mode: fs.FileMode(h.Mode).Perm(), link: h.Linkname}
		if e.typ == tar.TypeReg {
			if e.data, err = b.readAll(tr); err != nil {
				return nil, err
			}
		} else if err := b.take(0); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
}

func readZip(data []byte) ([]entry, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, &Error{Kind: KindInvalid, Code: ErrUnknownFormat.Code, Fields: []FieldError{{Field: "body", Code: "corrupt", Detail: err.Error()}}}
	}
	var out []entry
	var b budget
	for _, f := range zr.File {
		name, err := entryName(f.Name)
		if err != nil {
			return nil, err
		}
		if name == "" {
			continue
		}
		mode := f.Mode()
		e := entry{name: name, mode: mode.Perm()}
		switch {
		case mode.IsDir():
			e.typ = tar.TypeDir
		case mode&fs.ModeSymlink != 0:
			e.typ = tar.TypeSymlink
		case mode.IsRegular():
			e.typ = tar.TypeReg
		default:
			e.typ = tar.TypeFifo // anything else: refused by the path safety rules as special
		}
		if e.typ == tar.TypeReg || e.typ == tar.TypeSymlink {
			rc, err := f.Open()
			if err != nil {
				return nil, &Error{Kind: KindInvalid, Code: ErrUnknownFormat.Code, Fields: []FieldError{{Field: name, Code: "corrupt", Detail: err.Error()}}}
			}
			content, err := b.readAll(rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
			if e.typ == tar.TypeSymlink {
				e.link = string(content)
			} else {
				e.data = content
			}
		} else if err := b.take(0); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// entryName checks an archive member's name before it is placed anywhere: relative, with
// forward slashes, no "." or ".." component, no drive letter. "" is the archive's root.
func entryName(name string) (string, error) {
	n := strings.TrimSuffix(strings.TrimPrefix(name, "./"), "/")
	bad := n != "" && (strings.HasPrefix(n, "/") || strings.Contains(n, "\\") || (len(n) >= 2 && n[1] == ':'))
	if !bad && n != "" {
		for _, part := range strings.Split(n, "/") {
			if part == "" || part == "." || part == ".." {
				bad = true
			}
		}
	}
	if bad {
		return "", unsafe(name, string(bundle.RuleNotRelative), "not a relative path inside the archive")
	}
	return n, nil
}

// stripRoot removes a single directory that holds everything, as archiving a folder produces
// ("unit-testing/SKILL.md"): the archive is of the resource's directory either way.
func stripRoot(es []entry) []entry {
	var top string
	for _, e := range es {
		first, _, nested := strings.Cut(e.name, "/")
		if !nested && e.typ != tar.TypeDir {
			return es // a file at the root
		}
		if top == "" {
			top = first
		} else if first != top {
			return es
		}
	}
	if top == "" {
		return es
	}
	out := es[:0:0]
	for _, e := range es {
		if e.name == top {
			continue
		}
		e.name = strings.TrimPrefix(e.name, top+"/")
		out = append(out, e)
	}
	return out
}

// writeTree writes a repository as a tar: rigdepot.yaml and the members under dir. Links stay
// links, so that the path safety rules refuse the resource with the rule that applies.
func writeTree(config []byte, dir string, es []entry) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(h *tar.Header, data []byte) error {
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := add(&tar.Header{Name: "rigdepot.yaml", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(config))}, config); err != nil {
		return nil, err
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		if err := add(&tar.Header{Name: strings.Join(parts[:i+1], "/") + "/", Typeflag: tar.TypeDir, Mode: 0o755}, nil); err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	for _, e := range es {
		name := path.Join(dir, e.name)
		if seen[name] {
			return nil, unsafe(e.name, "duplicate", "listed twice in the archive")
		}
		seen[name] = true
		h := &tar.Header{Name: name, Typeflag: e.typ, Mode: int64(e.mode), Linkname: e.link}
		switch e.typ {
		case tar.TypeReg:
			h.Size = int64(len(e.data))
		case tar.TypeDir:
			h.Name += "/"
			h.Mode = 0o755
		}
		if err := add(h, e.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// unsafe is the refusal of an upload that breaks a path safety rule.
func unsafe(file, rule, detail string) *Error {
	return &Error{Kind: KindInvalid, Code: ErrUnsafePath.Code, Params: map[string]any{"rule": rule},
		Fields: []FieldError{{Field: file, Code: rule, Detail: detail}}}
}
