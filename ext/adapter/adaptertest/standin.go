// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adaptertest

// The stand-ins for internal/executor and internal/sandbox, which are being written at the same
// time as this suite. They do the least that lets every assertion of the contract mean
// something: the safety checks the executor makes (prefix, inside the home, unchanged since the
// plan, the file parses), writes that leave every other byte alone, and undo that restores the
// bytes. They are not the product's executor and must not grow into one; see core.go for how
// they are replaced.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/brand"
)

// errAdapterDefect marks an operation no adapter may return.
var errAdapterDefect = errors.New("adapter defect")

// errChanged: the target changed between the plan and the write.
var errChanged = errors.New("the target changed after the plan was made")

type standInExecutor struct {
	home   string
	state  string
	places []string
	kept   int
}

func newStandInExecutor(home, state string, places []string) *standInExecutor {
	return &standInExecutor{home: home, state: state, places: places}
}

// standInUndo is what the stand-in records to undo an entry.
type standInUndo struct {
	createdDirs []string // directories it created, outermost first
	insertion   insertion
}

// resolve turns a path in the package form into a path on disk inside the home directory.
func (e *standInExecutor) resolve(p string) (string, error) {
	var rel string
	switch {
	case p == "~":
		rel = "."
	case strings.HasPrefix(p, "~/"):
		rel = p[2:]
	default:
		home := filepath.ToSlash(e.home)
		r, ok := strings.CutPrefix(path.Clean(p), strings.TrimSuffix(home, "/")+"/")
		if !ok {
			return "", fmt.Errorf("%w: %s is outside the home directory", errAdapterDefect, p)
		}
		rel = r
	}
	rel = path.Clean(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return "", fmt.Errorf("%w: %s leaves the home directory", errAdapterDefect, p)
	}
	return filepath.Join(e.home, filepath.FromSlash(rel)), nil
}

// checkOwned is the executor's ownership check: what we write carries the prefix.
func checkOwned(op adapter.Op) error {
	last := ""
	switch o := op.(type) {
	case adapter.WriteTree:
		last = path.Base(o.Dir)
	case adapter.WriteFile:
		last = path.Base(o.Path)
	case adapter.SetKey:
		if len(o.Path) == 0 {
			return fmt.Errorf("%w: a key without a path", errAdapterDefect)
		}
		if last = o.Path[len(o.Path)-1]; last == brand.MCPEntry {
			return nil
		}
		if !o.Format.Valid() {
			return fmt.Errorf("%w: unknown format %q", errAdapterDefect, o.Format)
		}
	case adapter.Exec:
		return nil
	default:
		return fmt.Errorf("%w: unknown operation %T", errAdapterDefect, op)
	}
	if !strings.HasPrefix(last, brand.Prefix) || len(last) == len(brand.Prefix) {
		return fmt.Errorf("%w: %q does not carry the prefix", errAdapterDefect, last)
	}
	return nil
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func treeDigest(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		sum := sha256.Sum256(files[n])
		fmt.Fprintf(&b, "%s\x00%x\n", n, sum)
	}
	return hashString(b.String())
}

func keyDigest(format adapter.Format, keyPath []string, value any) (string, error) {
	if format == adapter.TOML {
		block, err := tomlBlock(keyPath, value)
		return hashString(block), err
	}
	c, err := canonical(value)
	return hashString(c), err
}

func (e *standInExecutor) Expect(op adapter.Op) (string, error) {
	switch o := op.(type) {
	case adapter.WriteTree:
		files := map[string][]byte{}
		for _, f := range o.Files {
			files[f.Path] = f.Data
		}
		return treeDigest(files), nil
	case adapter.WriteFile:
		return hashString(string(o.Data)), nil
	case adapter.SetKey:
		return keyDigest(o.Format, o.Path, o.Value)
	}
	return "", fmt.Errorf("the stand-in executor does not handle %T", op)
}

func (e *standInExecutor) Observe(t target) observed {
	if t.kind == "exec" {
		return observed{state: absent}
	}
	full, err := e.resolve(t.path)
	if err != nil {
		return observed{state: unreadable, reason: err.Error()}
	}
	switch t.kind {
	case "tree":
		info, err := os.Stat(full)
		if errors.Is(err, fs.ErrNotExist) {
			return observed{state: absent}
		}
		if err != nil {
			return observed{state: unreadable, reason: err.Error()}
		}
		if !info.IsDir() {
			return observed{state: present, digest: "not a directory"}
		}
		files := map[string][]byte{}
		err = filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(p)
			rel, _ := filepath.Rel(full, p)
			files[filepath.ToSlash(rel)] = data
			return err
		})
		if err != nil {
			return observed{state: unreadable, reason: err.Error()}
		}
		return observed{state: present, digest: treeDigest(files)}
	case "file":
		data, err := os.ReadFile(full)
		if errors.Is(err, fs.ErrNotExist) {
			return observed{state: absent}
		}
		if err != nil {
			return observed{state: unreadable, reason: err.Error()}
		}
		return observed{state: present, digest: hashString(string(data))}
	case "key":
		data, err := os.ReadFile(full)
		if errors.Is(err, fs.ErrNotExist) {
			return observed{state: absent}
		}
		if err != nil {
			return observed{state: unreadable, reason: err.Error()}
		}
		if t.format == adapter.TOML {
			block, found := findTOMLBlock(data, t.key)
			if !found {
				return observed{state: absent}
			}
			return observed{state: present, digest: hashString(block)}
		}
		doc, err := decodeJSON(data, t.format)
		if err != nil {
			return observed{state: unreadable, reason: t.path + " does not parse"}
		}
		v, ok := lookup(doc, t.key)
		if !ok {
			return observed{state: absent}
		}
		c, _ := canonical(v)
		return observed{state: present, digest: hashString(c)}
	}
	return observed{state: unreadable, reason: "unknown target " + t.kind}
}

// mkdirs creates dir and its missing parents and returns those it created, outermost first.
func mkdirs(dir string) ([]string, error) {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append([]string{d}, missing...)
		if filepath.Dir(d) == d {
			break
		}
	}
	for _, d := range missing {
		if err := os.Mkdir(d, 0o755); err != nil {
			return nil, err
		}
	}
	return missing, nil
}

func (e *standInExecutor) Apply(op adapter.Op, pre observed) (entry, error) {
	t := targetOf(op)
	if err := checkOwned(op); err != nil {
		return entry{}, err
	}
	if t.kind == "exec" {
		return entry{}, errors.New("tests do not run the tool's own commands")
	}
	full, err := e.resolve(t.path)
	if err != nil {
		return entry{}, err
	}
	declared := false
	for _, p := range e.places {
		declared = declared || inside(path.Clean(t.path), p)
	}
	if !declared {
		return entry{}, fmt.Errorf("%w: %s lies outside the places the adapter declared", errAdapterDefect, t.path)
	}
	if now := e.Observe(t); now != pre {
		return entry{}, errChanged
	}
	if pre.state == unreadable {
		return entry{}, fmt.Errorf("%s cannot be read, so it is not written: %s", t.path, pre.reason)
	}
	var undo standInUndo
	switch o := op.(type) {
	case adapter.WriteTree:
		if undo.createdDirs, err = mkdirs(filepath.Dir(full)); err != nil {
			return entry{}, err
		}
		if err := os.RemoveAll(full); err != nil {
			return entry{}, err
		}
		for _, f := range o.Files {
			rel := path.Clean(f.Path)
			if f.Path == "" || path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
				return entry{}, fmt.Errorf("%w: the file %q leaves its tree", errAdapterDefect, f.Path)
			}
			p := filepath.Join(full, filepath.FromSlash(rel))
			if _, err := mkdirs(filepath.Dir(p)); err != nil {
				return entry{}, err
			}
			mode := fs.FileMode(0o644)
			if f.Exec {
				mode = 0o755
			}
			if err := os.WriteFile(p, f.Data, mode); err != nil {
				return entry{}, err
			}
		}
	case adapter.WriteFile:
		if undo.createdDirs, err = mkdirs(filepath.Dir(full)); err != nil {
			return entry{}, err
		}
		if err := os.WriteFile(full, o.Data, 0o644); err != nil {
			return entry{}, err
		}
	case adapter.SetKey:
		data, err := os.ReadFile(full)
		mode := fs.FileMode(0o644)
		switch {
		case err == nil:
			if info, err := os.Stat(full); err == nil {
				mode = info.Mode().Perm()
			}
		case errors.Is(err, fs.ErrNotExist):
			data = nil
		default:
			return entry{}, err
		}
		var out []byte
		if o.Format == adapter.TOML {
			out, undo.insertion, err = appendTOML(data, o.Path, o.Value)
		} else {
			out, undo.insertion, err = insertJSON(data, o.Format, o.Path, o.Value)
		}
		if err != nil {
			return entry{}, fmt.Errorf("%s: %w", t.path, err)
		}
		if data == nil {
			if undo.createdDirs, err = mkdirs(filepath.Dir(full)); err != nil {
				return entry{}, err
			}
		}
		if err := os.WriteFile(full, out, mode); err != nil {
			return entry{}, err
		}
	}
	after := e.Observe(t)
	want, err := e.Expect(op)
	if err != nil {
		return entry{}, err
	}
	if after.state != present || after.digest != want {
		return entry{}, fmt.Errorf("%s: what was written does not read back as planned", t)
	}
	return entry{target: t, ref: op.Ref(), digest: after.digest, undo: undo}, nil
}

func (e *standInExecutor) Revert(en entry) (bool, error) {
	undo, _ := en.undo.(standInUndo)
	full, err := e.resolve(en.target.path)
	if err != nil {
		return false, err
	}
	now := e.Observe(en.target)
	kept := false
	switch en.target.kind {
	case "tree", "file":
		if now.state == unreadable {
			return false, fmt.Errorf("%s cannot be read: %s", en.target, now.reason)
		}
		if now.state == present && now.digest != en.digest {
			e.kept++
			dst := filepath.Join(e.state, "kept", fmt.Sprint(e.kept), filepath.Base(full))
			if en.target.kind == "tree" {
				err = copyTree(full, dst)
			} else if err = os.MkdirAll(filepath.Dir(dst), 0o700); err == nil {
				var data []byte
				if data, err = os.ReadFile(full); err == nil {
					err = os.WriteFile(dst, data, 0o600)
				}
			}
			if err != nil {
				return false, err
			}
			kept = true
		}
		if err := os.RemoveAll(full); err != nil {
			return false, err
		}
	case "key":
		data, err := os.ReadFile(full)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return false, err
		}
		if now.state == present && now.digest != en.digest {
			return false, errors.New("the stand-in executor cannot remove a key whose value was changed by hand")
		}
		if now.state == absent {
			break
		}
		out, remove, err := undoInsertion(data, undo.insertion)
		if err != nil {
			return false, fmt.Errorf("%s: %w (the real executor falls back to removing the key by its meaning)", en.target, err)
		}
		if remove {
			err = os.Remove(full)
		} else {
			err = os.WriteFile(full, out, 0o644)
		}
		if err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("the stand-in executor cannot undo %s", en.target)
	}
	for i := len(undo.createdDirs) - 1; i >= 0; i-- {
		entries, err := os.ReadDir(undo.createdDirs[i])
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(undo.createdDirs[i]); err != nil {
			return kept, err
		}
	}
	return kept, nil
}

// standInSandbox copies the whole home, a superset of what the targets touch. The real sandbox
// copies only the targets, and never a credentials file.
type standInSandbox struct{ base func() string }

func (s standInSandbox) Prepare(home string, _ []target) (string, error) {
	dst := filepath.Join(s.base(), "home")
	return dst, copyTree(home, dst)
}
