// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package home

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
)

// EnvVar is the environment variable that moves the local data directory. Its value must be an
// absolute path.
const EnvVar = brand.EnvPrefix + "HOME"

// Permissions of what this package creates. Windows ignores them and the ACL is inherited.
const (
	DirPerm  fs.FileMode = 0o700
	FilePerm fs.FileMode = 0o600
)

// Names inside the local data directory.
const (
	ConfigFile   = "config.toml"
	ManifestFile = "manifest.json"
	// ManifestPrevious is the manifest as it was before the last write.
	ManifestPrevious = ManifestFile + ".1"
	JournalFile      = "manifest.journal"
	LockFile         = "lock"
	StateDir         = "state"
	CacheDir         = "cache"
	BackupDir        = "backup"
	KeptDir          = "kept"
	TmpDir           = "tmp"
	SecretsDir       = "secrets"
	LogsDir          = "logs"
	// FlowsDir holds the workflows synced to this computer and the runs in progress
	// (rgd flow next).
	FlowsDir = "flows"
	// KnowledgeDir holds the knowledge bases fetched onto this computer, one directory each.
	KnowledgeDir = "knowledge"
)

// Dir is the local data directory. The zero value is not usable; get one from Locate or At.
type Dir struct {
	path string
}

// At returns the local data directory at path, which must be absolute. Tests and previews use
// it; programs use Locate.
func At(path string) (Dir, error) {
	if !filepath.IsAbs(path) {
		return Dir{}, fmt.Errorf("the local data directory must be an absolute path, not %q", path)
	}
	return Dir{path: filepath.Clean(path)}, nil
}

// Locate finds the local data directory: the value of EnvVar when it is set, otherwise
// brand.Home inside the user's home directory. A relative value of EnvVar is an error rather
// than being ignored: the person asked for another place, and silently using the default would
// write where they did not want.
func Locate(getenv func(string) string, userHome func() (string, error)) (Dir, error) {
	if v := getenv(EnvVar); v != "" {
		if !filepath.IsAbs(v) {
			return Dir{}, fmt.Errorf("%s is %q; it has to be an absolute path", EnvVar, v)
		}
		return At(v)
	}
	h, err := userHome()
	if err != nil {
		return Dir{}, fmt.Errorf("cannot tell where the home directory is: %w", err)
	}
	return At(filepath.Join(h, brand.Home))
}

// Default is Locate with the process environment.
func Default() (Dir, error) {
	return Locate(os.Getenv, os.UserHomeDir)
}

// Path returns the absolute path of the directory.
func (d Dir) Path() string { return d.path }

// Join returns the path of name inside the directory. name uses forward slashes.
func (d Dir) Join(name ...string) string {
	parts := make([]string, 0, len(name)+1)
	parts = append(parts, d.path)
	for _, n := range name {
		parts = append(parts, filepath.FromSlash(n))
	}
	return filepath.Join(parts...)
}

// Ensure creates the directory if it does not exist and tightens its permissions to DirPerm if
// they are looser. The directory is ours, so tightening it changes nothing anyone else relies on.
func (d Dir) Ensure() error {
	if d.path == "" {
		return errors.New("home: the local data directory is not set")
	}
	if err := os.MkdirAll(d.path, DirPerm); err != nil {
		return err
	}
	return tighten(d.path)
}

// EnsureSub creates a directory inside the local data directory, with DirPerm.
func (d Dir) EnsureSub(name string) (string, error) {
	if err := d.Ensure(); err != nil {
		return "", err
	}
	p := d.Join(name)
	if err := os.MkdirAll(p, DirPerm); err != nil {
		return "", err
	}
	return p, tighten(p)
}

func tighten(p string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", p)
	}
	if fi.Mode().Perm()&^DirPerm != 0 {
		return os.Chmod(p, DirPerm)
	}
	return nil
}

// stampLayout is the time format of backup/<time>/ and kept/<time>/. It has no colons, which
// Windows does not allow in file names.
const stampLayout = "2006-01-02T15-04-05"

// NewStamped creates a new, empty directory under parent (BackupDir or KeptDir) named after
// the time, such as kept/2026-11-24T10-02-11, and returns its absolute path and its path
// relative to the local data directory with forward slashes. When a directory of that name
// already exists, a counter is added; an existing directory is never reused, so that two runs
// in the same second do not mix their files.
func (d Dir) NewStamped(parent string, now time.Time) (abs, rel string, err error) {
	base, err := d.EnsureSub(parent)
	if err != nil {
		return "", "", err
	}
	stamp := now.UTC().Format(stampLayout)
	for i := 1; i < 1000; i++ {
		name := stamp
		if i > 1 {
			name += "-" + strconv.Itoa(i)
		}
		p := filepath.Join(base, name)
		err := os.Mkdir(p, DirPerm)
		if err == nil {
			return p, parent + "/" + name, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("home: too many directories named %s in %s", stamp, base)
}
