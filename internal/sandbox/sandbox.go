// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sandbox

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
)

// dirPrefix starts the name of every sandbox directory, so that leftovers can be recognised.
const dirPrefix = "plan-"

// StaleAfter is how old a leftover sandbox has to be before New removes it. A preview takes
// seconds; the margin keeps one run from removing the sandbox of another that runs at the same
// time.
const StaleAfter = time.Hour

// ErrTarget means a target cannot be previewed: it is not written as "~/…" or as an absolute
// path, it leaves the home directory with "..", it is the home directory itself, or it lies
// inside the sandbox directory.
var ErrTarget = errors.New("target cannot be previewed")

// ErrCleanup means the preview worked but its sandbox could not be removed completely. The
// result is valid; the leftover is removed by a later run.
var ErrCleanup = errors.New("the preview's temporary directory could not be removed")

// Options configures a sandbox.
type Options struct {
	// TmpDir is the directory sandboxes are created in: tmp/ in the local data directory, which
	// is on the same file system as the files it copies. It is created when missing.
	TmpDir string
	// Home is the real home directory, absolute.
	Home string
	// Credentials lists file names that are never copied, from the Credentials of every
	// adapter's profile. Names are compared without regard to case.
	Credentials []string
}

// Applier carries out a plan inside a sandbox. In the command line it is the executor, built
// with box.Home() as the home root, box.Path of each tool root outside the home directory as the
// other roots, and box.DataDir() as the place for its backups, kept files and intent log, so
// that nothing outside the sandbox is touched. It applies ops and undoes the manifest entries
// the run removes (their paths mapped with box.Path).
//
// The paths in ops already point into the sandbox: "~/…" means box.Home(), and absolute paths
// outside the home directory have been replaced by their place in the sandbox. Exec operations
// have been taken out.
type Applier interface {
	Apply(ctx context.Context, box *Sandbox, ops []adapter.Op) error
}

// ApplierFunc lets an ordinary function be an Applier.
type ApplierFunc func(ctx context.Context, box *Sandbox, ops []adapter.Op) error

// Apply implements Applier.
func (f ApplierFunc) Apply(ctx context.Context, box *Sandbox, ops []adapter.Op) error {
	return f(ctx, box, ops)
}

// Request is what to preview.
type Request struct {
	// Ops are the operations of the new plan.
	Ops []adapter.Op
	// Touches lists further paths the run changes, written like the targets of operations: the
	// paths of the manifest entries it undoes.
	Touches []string
}

// Result is what a preview found.
type Result struct {
	// Changes lists every path that was created, modified or removed, sorted by path.
	Changes []Change
	// NotRun are the Exec operations of the plan, which a preview does not carry out.
	NotRun []adapter.Exec
	// Withheld lists the targets, and the files inside targets, that were not copied because
	// they are credential files, as "~/…" or absolute paths. A change at one of these paths is
	// marked Withheld: the preview did not see the original.
	Withheld []string
}

// Preview runs req in a new sandbox and returns what changed. The sandbox is removed before it
// returns. When only the removal fails, the result is returned together with an error that
// satisfies errors.Is(err, ErrCleanup).
func Preview(ctx context.Context, opts Options, req Request, a Applier) (res Result, err error) {
	box, err := New(opts)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if cerr := box.Close(); cerr != nil {
			if err == nil {
				err = cerr
			} else {
				err = errors.Join(err, cerr)
			}
		}
	}()

	ops, notRun := box.rewrite(req.Ops)
	targets := slices.Concat(Targets(req.Ops), req.Touches)
	// Rewriting checks the targets of operations; Copy checks them all again.
	if err := box.Copy(targets); err != nil {
		return Result{}, err
	}
	before, err := box.Snapshot()
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := a.Apply(ctx, box, ops); err != nil {
		return Result{}, err
	}
	after, err := box.Snapshot()
	if err != nil {
		return Result{}, err
	}
	return Result{
		Changes:  box.Diff(before, after),
		NotRun:   notRun,
		Withheld: box.Withheld(),
	}, nil
}

// Targets returns the target of every operation, as written in it. Exec operations have none.
func Targets(ops []adapter.Op) []string {
	var out []string
	for _, op := range ops {
		switch o := op.(type) {
		case adapter.WriteTree:
			out = append(out, o.Dir)
		case adapter.WriteFile:
			out = append(out, o.Path)
		case adapter.SetKey:
			out = append(out, o.File)
		}
	}
	return out
}

// Sandbox is one temporary home directory.
type Sandbox struct {
	root        string // the sandbox directory
	home        string // root/home: stands for the real home directory
	ext         string // root/ext: absolute paths outside the home directory, by volume
	data        string // root/data: the executor's own files during the preview
	tmpDir      string
	realHome    string
	credentials []string
	withheld    map[string]bool // targets as written ("~/…" or absolute), not copied
}

// New creates an empty sandbox. It also removes sandboxes older than StaleAfter that earlier
// runs left behind; failing to remove those does not fail New.
func New(opts Options) (*Sandbox, error) {
	if opts.TmpDir == "" || !filepath.IsAbs(opts.TmpDir) {
		return nil, fmt.Errorf("sandbox: the temporary directory must be an absolute path, not %q", opts.TmpDir)
	}
	if opts.Home == "" || !filepath.IsAbs(opts.Home) {
		return nil, fmt.Errorf("sandbox: the home directory must be an absolute path, not %q", opts.Home)
	}
	if err := os.MkdirAll(opts.TmpDir, 0o700); err != nil {
		return nil, err
	}
	_ = CleanStale(opts.TmpDir, StaleAfter)
	root, err := os.MkdirTemp(opts.TmpDir, dirPrefix)
	if err != nil {
		return nil, err
	}
	b := &Sandbox{
		root:        root,
		home:        filepath.Join(root, "home"),
		ext:         filepath.Join(root, "ext"),
		data:        filepath.Join(root, "data"),
		tmpDir:      filepath.Clean(opts.TmpDir),
		realHome:    filepath.Clean(opts.Home),
		credentials: opts.Credentials,
		withheld:    map[string]bool{},
	}
	for _, d := range []string{b.home, b.ext, b.data} {
		if err := os.Mkdir(d, 0o700); err != nil {
			os.RemoveAll(root)
			return nil, err
		}
	}
	return b, nil
}

// CleanStale removes sandboxes in tmpDir that are older than age. A tmpDir that does not exist
// has nothing to clean.
func CleanStale(tmpDir string, age time.Duration) error {
	entries, err := os.ReadDir(tmpDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	cutoff := time.Now().Add(-age)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), dirPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(tmpDir, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Home returns the sandbox's home directory, the stand-in for the real one.
func (b *Sandbox) Home() string { return b.home }

// DataDir returns a directory inside the sandbox for the executor's own files (backups, kept
// files, intent log) during the preview. It is empty at the start and not part of the diff.
func (b *Sandbox) DataDir() string { return b.data }

// Close removes the sandbox.
func (b *Sandbox) Close() error {
	if err := os.RemoveAll(b.root); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrCleanup, b.root, err)
	}
	return nil
}

// location is a target resolved against the real computer and the sandbox.
type location struct {
	target string // "~/…" or absolute, forward slashes, cleaned
	real   string // on the real computer
	inBox  string // in the sandbox
	top    string // the directory on the real computer the mirroring starts from
	topBox string // its counterpart in the sandbox
}

// locate resolves a target written "~/…" or absolute.
func (b *Sandbox) locate(target string) (location, error) {
	refuse := func(why string) (location, error) {
		return location{}, fmt.Errorf("%w: %q: %s", ErrTarget, target, why)
	}
	var rel string
	if target == "~" || strings.HasPrefix(target, "~/") {
		rel = strings.TrimPrefix(strings.TrimPrefix(target, "~"), "/")
		if strings.Contains(rel, `\`) {
			return refuse("backslashes are not path separators here")
		}
		rel = path.Clean(rel)
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			return refuse("not inside the home directory")
		}
	} else {
		native := filepath.Clean(filepath.FromSlash(target))
		if !filepath.IsAbs(native) {
			return refuse(`not written as "~/…" or as an absolute path`)
		}
		r, err := filepath.Rel(b.realHome, native)
		if err == nil && filepath.IsLocal(r) {
			rel = filepath.ToSlash(r)
		} else if err == nil && r == "." {
			return refuse("the home directory itself")
		} else {
			return b.locateOutside(native)
		}
	}
	if rel == "." {
		return refuse("the home directory itself")
	}
	loc := location{
		target: "~/" + rel,
		real:   filepath.Join(b.realHome, filepath.FromSlash(rel)),
		inBox:  filepath.Join(b.home, filepath.FromSlash(rel)),
		top:    b.realHome,
		topBox: b.home,
	}
	if overlaps(loc.real, b.tmpDir) {
		return refuse("it contains the sandbox directory or lies inside it")
	}
	return loc, nil
}

// locateOutside places an absolute path outside the home directory under ext/<volume>/ in the
// sandbox. The volume ("" on Unix, "C:" or a UNC share on Windows) is hex-encoded so that the
// path can be translated back.
func (b *Sandbox) locateOutside(native string) (location, error) {
	vol := filepath.VolumeName(native)
	rest := strings.TrimLeft(native[len(vol):], `/\`)
	if rest == "" {
		return location{}, fmt.Errorf("%w: %q: the root of a file system", ErrTarget, filepath.ToSlash(native))
	}
	key := "v" + hex.EncodeToString([]byte(vol))
	loc := location{
		target: filepath.ToSlash(native),
		real:   native,
		inBox:  filepath.Join(b.ext, key, rest),
		top:    vol + string(filepath.Separator),
		topBox: filepath.Join(b.ext, key),
	}
	if overlaps(loc.real, b.tmpDir) {
		return location{}, fmt.Errorf("%w: %q: it contains the sandbox directory or lies inside it", ErrTarget, loc.target)
	}
	return loc, nil
}

// overlaps reports whether a and b are the same directory or one contains the other.
func overlaps(a, b string) bool {
	within := func(base, p string) bool {
		r, err := filepath.Rel(base, p)
		return err == nil && (r == "." || filepath.IsLocal(r))
	}
	return within(a, b) || within(b, a)
}

// Path returns where a target, written "~/…" or absolute, lies inside the sandbox.
func (b *Sandbox) Path(target string) (string, error) {
	loc, err := b.locate(target)
	if err != nil {
		return "", err
	}
	return loc.inBox, nil
}

// Real translates a path inside the sandbox back. It returns the path as written in operations
// ("~/…" or absolute, forward slashes) and as it is on the real computer. ok is false for a path
// that does not stand for anything on the real computer (the sandbox's own directories, DataDir).
func (b *Sandbox) Real(inBox string) (target, real string, ok bool) {
	if r, err := filepath.Rel(b.home, inBox); err == nil && filepath.IsLocal(r) {
		return "~/" + filepath.ToSlash(r), filepath.Join(b.realHome, r), true
	}
	r, err := filepath.Rel(b.ext, inBox)
	if err != nil || !filepath.IsLocal(r) {
		return "", "", false
	}
	key, rest, found := strings.Cut(r, string(filepath.Separator))
	if !found || !strings.HasPrefix(key, "v") {
		return "", "", false
	}
	vol, err := hex.DecodeString(key[1:])
	if err != nil {
		return "", "", false
	}
	real = string(vol) + string(filepath.Separator) + rest
	return filepath.ToSlash(real), real, true
}

// rewrite returns the operations as they are carried out in the sandbox: absolute targets inside
// the home directory written "~/…", absolute targets outside it replaced by their place in the
// sandbox, Exec operations taken out. A target that cannot be located is left as it is; Copy
// refuses it before anything runs.
func (b *Sandbox) rewrite(ops []adapter.Op) ([]adapter.Op, []adapter.Exec) {
	var out []adapter.Op
	var notRun []adapter.Exec
	inBox := func(target string) string {
		loc, err := b.locate(target)
		switch {
		case err != nil:
			return target
		case strings.HasPrefix(loc.target, "~/"):
			return loc.target
		default:
			return filepath.ToSlash(loc.inBox)
		}
	}
	for _, op := range ops {
		switch o := op.(type) {
		case adapter.WriteTree:
			o.Dir = inBox(o.Dir)
			out = append(out, o)
		case adapter.WriteFile:
			o.Path = inBox(o.Path)
			out = append(out, o)
		case adapter.SetKey:
			o.File = inBox(o.File)
			out = append(out, o)
		case adapter.Exec:
			notRun = append(notRun, o)
		default:
			out = append(out, op)
		}
	}
	return out, notRun
}

// Withheld returns the paths that were not copied because they are credential files, sorted.
func (b *Sandbox) Withheld() []string {
	out := make([]string, 0, len(b.withheld))
	for p := range b.withheld {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// isCredential reports whether a file name is one of the credential file names.
func (b *Sandbox) isCredential(name string) bool {
	for _, c := range b.credentials {
		if c != "" && strings.EqualFold(name, c) {
			return true
		}
	}
	return false
}
