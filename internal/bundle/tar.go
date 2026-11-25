// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package bundle

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"
)

// Packed is a resource bundle: the bytes of the archive and its address.
type Packed struct {
	Data []byte
	// Address is "sha256:" and the hexadecimal SHA-256 of Data. It addresses the archive's
	// bytes; the resource's content digest is Digest of its files.
	Address string
}

// epoch is the modification time of every entry.
var epoch = time.Unix(0, 0)

// Pack builds the bundle of the files below the root of fsys. The files go through Snapshot
// first, so a bundle never holds anything the path safety checks refuse.
func Pack(fsys fs.FS) (Packed, error) {
	files, err := Snapshot(fsys)
	if err != nil {
		return Packed{}, err
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dirs := map[string]bool{}
	for _, p := range files.Paths() {
		// Every parent directory before its contents. Paths are in byte order and a
		// directory's own name with its slash sorts before anything in it.
		for i, part := range strings.Split(path.Dir(p), "/") {
			if part == "." {
				break
			}
			dir := strings.Join(strings.Split(p, "/")[:i+1], "/")
			if dirs[dir] {
				continue
			}
			dirs[dir] = true
			if err := tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir, Name: dir + "/", Mode: 0o755, ModTime: epoch,
			}); err != nil {
				return Packed{}, err
			}
		}
		e := files.entries[p]
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: p, Mode: int64(e.mode.Perm()), Size: int64(len(e.data)), ModTime: epoch,
		}); err != nil {
			return Packed{}, err
		}
		if _, err := tw.Write(e.data); err != nil {
			return Packed{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return Packed{}, err
	}
	sum := sha256.Sum256(buf.Bytes())
	return Packed{Data: buf.Bytes(), Address: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

// Unpack reads a bundle into memory. Every entry must be a regular file or a directory and
// every path must pass the path safety checks; otherwise it returns an *UnsafeError listing
// every broken rule and nothing is unpacked. It reads at most MaxBytes of file contents.
func Unpack(r io.Reader) (*Files, error) {
	var c checker
	out := newFiles()
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bundle: reading the archive: %w", err)
		}
		name := strings.TrimSuffix(h.Name, "/")
		placeable := c.name(name)
		fi := h.FileInfo()
		switch h.Typeflag {
		case tar.TypeReg:
		case tar.TypeDir:
			if placeable {
				if err := out.add(name, &entry{mode: fs.ModeDir | 0o755}); err != nil {
					c.add(name, RuleNotRelative, "%v", err)
				}
			}
			continue
		case tar.TypeLink, tar.TypeSymlink:
			c.add(name, RuleLink, "is a link")
			continue
		default:
			c.add(name, RuleSpecial, "is not a regular file or directory (tar type %q)", h.Typeflag)
			continue
		}
		if !placeable {
			continue
		}
		if !c.file(h.Size) {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, h.Size))
		if err != nil {
			return nil, fmt.Errorf("bundle: reading %s: %w", name, err)
		}
		if err := out.add(name, &entry{mode: fileMode(fi.Mode()), data: data}); err != nil {
			c.add(name, RuleNotRelative, "%v", err)
		}
	}
	if err := c.result(); err != nil {
		return nil, err
	}
	return out, nil
}

// MaxTreeBytes is the most file content ReadTree reads from one archive.
const MaxTreeBytes = 512 << 20

// ErrTreeTooLarge is returned by ReadTree for an archive with more than MaxTreeBytes of content.
var ErrTreeTooLarge = errors.New("bundle: the archive is too large")

// ReadTree reads a whole archive, such as the output of git archive, into memory as it is.
// Unlike Unpack it keeps symbolic links, hard links and special files as entries (they cannot
// be opened, but ReadDir lists them with their type), so that taking one resource out of the
// tree with Snapshot refuses that resource and leaves the others readable.
//
// What it does refuse is an archive that cannot be laid out as a tree at all: an absolute path,
// a "." or ".." component, an entry that appears twice, or a file where a directory has to be.
// Git never writes such an archive. A pax global header (git archive puts the commit there) is
// skipped.
func ReadTree(r io.Reader) (*Files, error) {
	out := newFiles()
	tr := tar.NewReader(r)
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("bundle: reading the archive: %w", err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := strings.TrimSuffix(h.Name, "/")
		if !fs.ValidPath(name) || name == "." {
			return nil, fmt.Errorf("bundle: the archive has an entry %q that is not a relative path", h.Name)
		}
		fi := h.FileInfo()
		e := &entry{mode: fs.ModeIrregular, sys: h}
		switch h.Typeflag {
		case tar.TypeSymlink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			e.mode = fi.Mode().Type()
		case tar.TypeDir:
			e.mode = fs.ModeDir | 0o755
		case tar.TypeReg:
			total += h.Size
			if total > MaxTreeBytes {
				return nil, ErrTreeTooLarge
			}
			if e.data, err = io.ReadAll(io.LimitReader(tr, h.Size)); err != nil {
				return nil, fmt.Errorf("bundle: reading %s: %w", name, err)
			}
			e.mode = fileMode(fi.Mode())
		}
		if err := out.add(name, e); err != nil {
			return nil, fmt.Errorf("bundle: the archive entry %q %v", h.Name, err)
		}
	}
}
