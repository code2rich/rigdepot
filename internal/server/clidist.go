// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// cliDist reads the command-line program this server hands out from a directory of release
// archives, the files a release publishes: rgd_<version>_<os>_<arch>.tar.gz (or .zip), each
// with the binary at its top level, and optionally the release's checksums.txt. The packages
// and the image fill /usr/share/rigdepot/cli with the archives of their own version; an
// administrator can copy a release's files into <data_dir>/cli, which is searched first.
//
// The directories are read again on every request, so that a change needs no restart; what
// it learnt about an archive (the binary's checksum and size) is kept while the archive's
// size and modification time stay the same.
type cliDist struct {
	dirs []string
	log  *slog.Logger

	mu    sync.Mutex
	known map[string]cliBinary // by archive path
	last  string               // what the previous scan found, to log only changes
}

// cliBinary is one binary inside a release archive.
type cliBinary struct {
	os, arch string
	file     string // the name it is downloaded as: rgd-<os>-<arch>, .exe on Windows
	version  string // from the archive's name
	archive  string // path of the archive
	entry    string // name of the binary inside it
	sha256   string // of the binary
	size     int64  // of the binary

	// The archive's checksum, size and modification time when it was read.
	archiveSHA256 string
	archiveSize   int64
	modTime       time.Time
}

// cliSet is what a scan found: the binaries of one version, ordered by system and
// architecture.
type cliSet struct {
	dir      string
	version  string
	binaries []cliBinary
}

func newCLIDist(dirs []string, log *slog.Logger) *cliDist {
	return &cliDist{dirs: dirs, log: log, known: map[string]cliBinary{}}
}

// cliArchiveName matches a release archive of the command line: rgd_0.4.0_linux_amd64.tar.gz.
var cliArchiveName = regexp.MustCompile(`^` + regexp.QuoteMeta(brand.CLI) +
	`_([0-9A-Za-z.+-]+)_(darwin|linux|windows)_(amd64|arm64)\.(tar\.gz|zip)$`)

const cliChecksums = "checksums.txt"

// scan reads the first directory that holds at least one archive. A directory that does not
// exist is skipped; one that cannot be read is an error, as is a checksums.txt that cannot
// be read: what the server hands out then is unknown. An archive that does not match
// checksums.txt, is not listed there or holds no binary is left out and logged.
//
// When the directory holds archives of several versions, the newest version is handed out.
func (d *cliDist) scan() (cliSet, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, dir := range d.dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return cliSet{}, err
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() && cliArchiveName.MatchString(e.Name()) {
				names = append(names, e.Name())
			}
		}
		if len(names) == 0 {
			continue
		}
		sums, err := readCLIChecksums(filepath.Join(dir, cliChecksums))
		if err != nil {
			return cliSet{}, err
		}
		set, problems := d.read(dir, names, sums)
		for path := range d.known {
			if filepath.Dir(path) != dir || !slices.Contains(names, filepath.Base(path)) {
				delete(d.known, path)
			}
		}
		d.report(set, problems)
		return set, nil
	}
	d.report(cliSet{}, nil)
	return cliSet{}, nil
}

// read reads the archives names in dir; sums is checksums.txt (nil without one).
func (d *cliDist) read(dir string, names []string, sums map[string]string) (cliSet, []string) {
	var problems []string
	byVersion := map[string][]cliBinary{}
	for _, name := range names {
		b, err := d.readArchive(filepath.Join(dir, name), sums)
		if err != nil {
			problems = append(problems, name+": "+err.Error())
			continue
		}
		byVersion[b.version] = append(byVersion[b.version], b)
	}
	var versions []string
	for v := range byVersion {
		versions = append(versions, v)
	}
	if len(versions) == 0 {
		return cliSet{dir: dir}, problems
	}
	slices.SortFunc(versions, func(a, b string) int {
		if c := settings.CompareVersions(a, b); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	newest := versions[len(versions)-1]
	for _, v := range versions[:len(versions)-1] {
		problems = append(problems, fmt.Sprintf("version %s left out: %s is newer", v, newest))
	}
	set := cliSet{dir: dir, version: newest}
	seen := map[string]bool{}
	bins := byVersion[newest]
	slices.SortFunc(bins, func(a, b cliBinary) int { return strings.Compare(a.archive, b.archive) })
	for _, b := range bins {
		if seen[b.file] {
			problems = append(problems, filepath.Base(b.archive)+": a second archive for "+b.os+"/"+b.arch+", left out")
			continue
		}
		seen[b.file] = true
		set.binaries = append(set.binaries, b)
	}
	slices.SortFunc(set.binaries, func(a, b cliBinary) int { return strings.Compare(a.file, b.file) })
	return set, problems
}

// readArchive returns the binary in an archive, from what is known about it when the archive
// has not changed since, and checks the archive against checksums.txt when there is one.
func (d *cliDist) readArchive(path string, sums map[string]string) (cliBinary, error) {
	info, err := os.Stat(path)
	if err != nil {
		return cliBinary{}, err
	}
	b, ok := d.known[path]
	if !ok || b.archiveSize != info.Size() || !b.modTime.Equal(info.ModTime()) {
		delete(d.known, path)
		if b, err = readCLIArchive(path, info); err != nil {
			return cliBinary{}, err
		}
		d.known[path] = b
	}
	if sums != nil {
		name := filepath.Base(path)
		want, listed := sums[name]
		switch {
		case !listed:
			return cliBinary{}, errors.New("not listed in " + cliChecksums)
		case want != b.archiveSHA256:
			return cliBinary{}, fmt.Errorf("SHA-256 %s, %s says %s", b.archiveSHA256, cliChecksums, want)
		}
	}
	return b, nil
}

// readCLIArchive reads an archive: its checksum, and the binary's.
func readCLIArchive(path string, info fs.FileInfo) (cliBinary, error) {
	m := cliArchiveName.FindStringSubmatch(filepath.Base(path))
	b := cliBinary{
		version: m[1], os: m[2], arch: m[3], archive: path,
		file:        brand.CLI + "-" + m[2] + "-" + m[3],
		entry:       brand.CLI,
		archiveSize: info.Size(), modTime: info.ModTime(),
	}
	if b.os == "windows" {
		b.file += ".exe"
		b.entry += ".exe"
	}
	var err error
	if b.archiveSHA256, err = fileDigest(path); err != nil {
		return cliBinary{}, err
	}
	rc, size, err := openArchiveEntry(path, b.entry)
	if err != nil {
		return cliBinary{}, err
	}
	defer rc.Close()
	h := sha256.New()
	n, err := io.Copy(h, rc)
	if err != nil {
		return cliBinary{}, err
	}
	if size >= 0 && n != size {
		return cliBinary{}, fmt.Errorf("%s is %d bytes, the archive says %d", b.entry, n, size)
	}
	b.sha256, b.size = hex.EncodeToString(h.Sum(nil)), n
	return b, nil
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// readCLIChecksums reads a release's checksums.txt, "<sha256>  <file name>" per line; nil
// when there is none.
func readCLIChecksums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sums := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			return nil, fmt.Errorf("%s: malformed line %q", path, sc.Text())
		}
		sums[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return sums, nil
}

// report logs what a scan found when it differs from the previous one.
func (d *cliDist) report(set cliSet, problems []string) {
	var files []string
	for _, b := range set.binaries {
		files = append(files, b.file)
	}
	state := set.dir + "|" + set.version + "|" + strings.Join(files, ",") + "|" + strings.Join(problems, ";")
	if state == d.last {
		return
	}
	d.last = state
	for _, p := range problems {
		d.log.Error("command-line archive left out", "dir", set.dir, "problem", p)
	}
	if len(set.binaries) == 0 {
		d.log.Info("no command-line archives to hand out", "dirs", strings.Join(d.dirs, ","))
		return
	}
	d.log.Info("handing out the command line", "dir", set.dir, "version", set.version, "files", strings.Join(files, ","))
}

// open opens the binary for reading, from its archive.
func (b cliBinary) open() (io.ReadCloser, error) {
	rc, _, err := openArchiveEntry(b.archive, b.entry)
	return rc, err
}

// openArchiveEntry opens the regular file entry at the top level of a .tar.gz or .zip
// archive, and returns its size (-1 when the archive does not say).
func openArchiveEntry(path, entry string) (io.ReadCloser, int64, error) {
	if strings.HasSuffix(path, ".zip") {
		z, err := zip.OpenReader(path)
		if err != nil {
			return nil, 0, err
		}
		for _, f := range z.File {
			if f.Name == entry && f.Mode().IsRegular() {
				rc, err := f.Open()
				if err != nil {
					z.Close()
					return nil, 0, err
				}
				return readCloser{rc, func() error { rc.Close(); return z.Close() }}, int64(f.UncompressedSize64), nil
			}
		}
		z.Close()
		return nil, 0, fmt.Errorf("no %s in the archive", entry)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			f.Close()
			return nil, 0, fmt.Errorf("no %s in the archive", entry)
		}
		if err != nil {
			f.Close()
			return nil, 0, err
		}
		if strings.TrimPrefix(h.Name, "./") == entry && h.Typeflag == tar.TypeReg {
			return readCloser{tr, f.Close}, h.Size, nil
		}
	}
}

type readCloser struct {
	io.Reader
	close func() error
}

func (r readCloser) Close() error { return r.close() }
