// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package keyring

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"

	"github.com/code2rich/rigdepot/internal/home"
)

// fileStore keeps each entry in a file of its name in the secrets directory of the local data
// directory. On macOS and Linux the file is 0600 and is refused when its permissions have been
// loosened; on Windows its content is encrypted for the current user with DPAPI (protect).
type fileStore struct {
	dir home.Dir
}

// fileKind is the kind of the file fallback on this platform.
var fileKind = func() Kind {
	if runtime.GOOS == "windows" {
		return Protected
	}
	return PlainFile
}()

func (f fileStore) path(name string) string { return f.dir.Join(home.SecretsDir, name) }

func (f fileStore) get(name string) ([]byte, error) {
	p := f.path(name)
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	if err := private(p, fi); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	value, err := unprotect(data)
	if err != nil {
		return nil, fmt.Errorf("keyring: %s cannot be decrypted: %w", p, err)
	}
	return value, nil
}

func (f fileStore) set(name string, value []byte) error {
	if _, err := f.dir.EnsureSub(home.SecretsDir); err != nil {
		return err
	}
	data, err := protect(value)
	if err != nil {
		return fmt.Errorf("keyring: encrypting %s: %w", name, err)
	}
	return home.WriteFileAtomic(f.path(name), data, home.FilePerm)
}

func (f fileStore) delete(name string) error {
	err := os.Remove(f.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// entries returns the names of the entries kept in files. It does not look at what is in them.
func (f fileStore) entries() ([]string, error) {
	var found []string
	for _, name := range names {
		_, err := os.Lstat(f.path(name))
		switch {
		case err == nil:
			found = append(found, name)
		case !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
	}
	return found, nil
}

// insecure returns the paths of the entry files that are not private.
func (f fileStore) insecure() ([]string, error) {
	var bad []string
	for _, name := range names {
		p := f.path(name)
		fi, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, err
		}
		if private(p, fi) != nil {
			bad = append(bad, p)
		}
	}
	return bad, nil
}

// private refuses a file that is not a regular file (a link could point anywhere) or, on macOS
// and Linux, one that others than its owner can open.
func private(p string, fi fs.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrInsecure, p)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&^home.FilePerm != 0 {
		return fmt.Errorf("%w: others can open %s (permissions %04o, want %04o)", ErrInsecure, p, uint32(fi.Mode().Perm()), uint32(home.FilePerm))
	}
	return nil
}
