// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package home

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func TestLocateUsesTheVariableOrTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	noVar := func(string) string { return "" }
	userHome := func() (string, error) { return home, nil }

	d, err := Locate(noVar, userHome)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, brand.Home); d.Path() != want {
		t.Errorf("without %s: %s, want %s", EnvVar, d.Path(), want)
	}

	elsewhere := filepath.Join(t.TempDir(), "data")
	d, err = Locate(func(k string) string {
		if k == EnvVar {
			return elsewhere
		}
		return ""
	}, userHome)
	if err != nil {
		t.Fatal(err)
	}
	if d.Path() != elsewhere {
		t.Errorf("with %s: %s, want %s", EnvVar, d.Path(), elsewhere)
	}
}

func TestLocateRefusesARelativeVariable(t *testing.T) {
	_, err := Locate(func(string) string { return "relative/dir" }, func() (string, error) {
		t.Fatal("fell back to the home directory although the person asked for another place")
		return "", nil
	})
	if err == nil || !strings.Contains(err.Error(), EnvVar) {
		t.Fatalf("Locate = %v, want an error naming %s", err, EnvVar)
	}
}

func TestLocateFailsWhenTheHomeDirectoryIsUnknown(t *testing.T) {
	_, err := Locate(func(string) string { return "" }, func() (string, error) { return "", errors.New("no HOME") })
	if err == nil {
		t.Fatal("Locate succeeded without a home directory")
	}
}

func TestDefaultIsInsideTheTemporaryHome(t *testing.T) {
	d, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d.Path(), testenv.Current(t).Home) {
		t.Fatalf("Default() = %s, outside the temporary home %s", d.Path(), testenv.Current(t).Home)
	}
}

func TestEnsureCreatesAPrivateDirectoryAndTightensALooseOne(t *testing.T) {
	d, err := At(filepath.Join(t.TempDir(), brand.Home))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Ensure(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return // the ACL is inherited; there are no permission bits to check
	}
	assertPerm(t, d.Path(), DirPerm)
	if err := os.Chmod(d.Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.Ensure(); err != nil {
		t.Fatal(err)
	}
	assertPerm(t, d.Path(), DirPerm)

	sub, err := d.EnsureSub(KeptDir)
	if err != nil {
		t.Fatal(err)
	}
	assertPerm(t, sub, DirPerm)
}

func assertPerm(t *testing.T, p string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s has mode %v, want %v", p, got, want)
	}
}

func TestNewStampedNeverReusesADirectory(t *testing.T) {
	d, _ := At(filepath.Join(t.TempDir(), brand.Home))
	now := time.Date(2026, 11, 24, 10, 2, 11, 0, time.UTC)
	a, relA, err := d.NewStamped(KeptDir, now)
	if err != nil {
		t.Fatal(err)
	}
	b, relB, err := d.NewStamped(KeptDir, now)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two calls in the same second returned the same directory %s", a)
	}
	if relA != "kept/2026-11-24T10-02-11" || relB != "kept/2026-11-24T10-02-11-2" {
		t.Errorf("relative paths %q and %q", relA, relB)
	}
	if strings.ContainsRune(filepath.Base(a), ':') {
		t.Errorf("%s has a colon, which Windows does not allow", a)
	}
	if d.Join(relB) != b {
		t.Errorf("Join(%q) = %s, want %s", relB, d.Join(relB), b)
	}
}

func TestWriteFileAtomicReplacesWholeAndLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ManifestFile)
	if err := WriteFileAtomic(p, []byte("old"), FilePerm); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("new content"), FilePerm); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil || string(got) != "new content" {
		t.Fatalf("read back %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		assertPerm(t, p, FilePerm)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only the target", names)
	}
}

func TestWriteFileAtomicKeepsTheOldFileWhenItCannotReplaceIt(t *testing.T) {
	dir := t.TempDir()
	// The target is a non-empty directory: the rename at the end fails on every platform.
	p := filepath.Join(dir, ManifestFile)
	if err := os.MkdirAll(filepath.Join(p, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("x"), FilePerm); err == nil {
		t.Fatal("replacing a non-empty directory with a file succeeded")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != ManifestFile {
		t.Errorf("the temporary file was left behind: %v", entries)
	}
}
