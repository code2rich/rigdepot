// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
)

// MarkerFile is the file the product leaves at the root of a directory it wrote. It is not
// part of the content and does not count towards the digest.
const MarkerFile = brand.Home + ".json"

// ErrEmpty is returned by Digest for a resource without any file to digest.
var ErrEmpty = errors.New("bundle: no files to digest")

// Digest returns the content digest of the regular files below the root of fsys. MarkerFile at
// the root is left out, and so is everything at or below each path in exclude.
//
// Only paths and contents count. A symbolic link or any other entry that is not a regular file
// or a directory is an error, not something to skip: run the files through Snapshot first.
func Digest(fsys fs.FS, exclude ...string) (resource.Digest, error) {
	var paths []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != "." && slices.ContainsFunc(exclude, func(x string) bool { return p == x || strings.HasPrefix(p, x+"/") }) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
		case d.Type().IsRegular():
			if p != MarkerFile {
				paths = append(paths, p)
			}
		default:
			return fmt.Errorf("bundle: %s is not a regular file and has no digest", p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", ErrEmpty
	}
	// The order of the whole paths, not of a directory walk: "a-b" comes before "a/b".
	slices.Sort(paths)
	list := sha256.New()
	for _, p := range paths {
		sum, err := fileSum(fsys, p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(list, "%s  %s\n", sum, p)
	}
	return resource.Digest("sha256:" + hex.EncodeToString(list.Sum(nil))), nil
}

func fileSum(fsys fs.FS, p string) (string, error) {
	f, err := fsys.Open(p)
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
