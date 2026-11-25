// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// MarkerFile is the file the executor puts in every directory it writes, saying who put the
// directory there. It is not part of the directory's digest.
const MarkerFile = brand.Home + ".json"

var (
	// ErrChanged: the target is no longer in the state the plan saw. Nothing was written.
	ErrChanged = errors.New("the target changed after the plan was made")
	// ErrUnreadable: the target could not be looked at, so nothing is done to it.
	ErrUnreadable = errors.New("the target could not be read")
	// ErrNotImplemented: the operation exists but the executor cannot carry it out yet.
	ErrNotImplemented = errors.New("this operation is not implemented yet")
	// ErrVerify: the target was written but does not read back as expected. The intent stays
	// in the log for recovery and the doctor command.
	ErrVerify = errors.New("the target does not read back as written")
	// ErrDefect is satisfied by every *DefectError.
	ErrDefect = errors.New("adapter defect")
)

// DefectError reports an operation that an adapter should never have returned: a target
// without the prefix, outside the tool's roots, or written the wrong way. It is a bug in the
// adapter, not something the person can fix.
type DefectError struct {
	Tool    string
	Target  string
	Problem string
}

func (e *DefectError) Error() string {
	return fmt.Sprintf("adapter %q: refusing %q: %s (this is a defect in the adapter)", e.Tool, e.Target, e.Problem)
}

func (e *DefectError) Is(target error) bool { return target == ErrDefect }

// Roots are the directories the executor may write in. Every file operation goes through an
// os.Root opened on one of them.
type Roots struct {
	// Home is the absolute path of the home directory: the real one, or the temporary copy a
	// preview runs in.
	Home string
	// Extra are absolute paths outside Home that tools' configuration roots resolve to, such as
	// a CLAUDE_CONFIG_DIR set to another disk.
	Extra []string
}

// Journal is where the executor announces a change before it makes it and records it when it
// is done. *manifest.Locked implements it; holding one means holding the lock.
type Journal interface {
	Intend(manifest.Intent) (manifest.Intent, error)
	// Commit records the change and returns the entry as recorded.
	Commit(manifest.Intent) (manifest.Entry, error)
	Forget(id string) error
}

// State is what an executor keeps besides its roots.
type State struct {
	// Data is the local data directory; changed targets are kept below its kept/ directory.
	Data home.Dir
	// Journal records the changes. Nil records nothing, for previews in a temporary home
	// directory: Apply still returns the entries it would have recorded.
	Journal Journal
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// Tool is the tool an operation is for, as detection found it.
type Tool struct {
	// ID is the adapter's identifier; it goes into the manifest.
	ID string
	// Roots are adapter.Detection.Roots. Every target has to lie strictly inside one of them.
	Roots map[string]string
}

// Executor carries out operations inside a set of roots.
type Executor struct {
	loc     locator
	roots   []*os.Root
	data    home.Dir
	journal Journal
	now     func() time.Time

	keptAbs, keptRel     string // kept/<time>/ of this executor, made on first use
	backupAbs, backupRel string // backup/<time>/ of this executor, made on first use

	// hook, when set by a test, runs at named steps of a write; an error from it is treated as
	// a failure at that step.
	hook func(step string) error
}

// New opens the roots. Close releases them.
func New(roots Roots, st State) (*Executor, error) {
	if !filepath.IsAbs(roots.Home) {
		return nil, fmt.Errorf("executor: the home directory %q is not absolute", roots.Home)
	}
	for _, r := range roots.Extra {
		if !filepath.IsAbs(r) {
			return nil, fmt.Errorf("executor: the root %q is not absolute", r)
		}
	}
	e := &Executor{
		loc:     newLocator(runtime.GOOS, filepath.ToSlash(roots.Home), toSlash(roots.Extra)),
		data:    st.Data,
		journal: st.Journal,
		now:     st.Now,
	}
	if e.journal == nil {
		e.journal = discard{}
	}
	if e.now == nil {
		e.now = time.Now
	}
	for _, r := range append([]string{roots.Home}, roots.Extra...) {
		root, err := os.OpenRoot(r)
		if err != nil {
			e.Close()
			return nil, fmt.Errorf("executor: opening %s: %w", r, err)
		}
		e.roots = append(e.roots, root)
	}
	return e, nil
}

func toSlash(ps []string) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = filepath.ToSlash(p)
	}
	return out
}

// Close releases the roots.
func (e *Executor) Close() error {
	var err error
	for _, r := range e.roots {
		err = errors.Join(err, r.Close())
	}
	e.roots = nil
	return err
}

// discard is the journal of a preview: it hands out ids, which name temporary files, and
// records nothing.
type discard struct{}

func (discard) Intend(in manifest.Intent) (manifest.Intent, error) {
	if in.ID == "" {
		var b [6]byte
		if _, err := rand.Read(b[:]); err != nil {
			return in, err
		}
		in.ID = hex.EncodeToString(b[:])
	}
	return in, nil
}

func (discard) Commit(in manifest.Intent) (manifest.Entry, error) { return in.Entry, nil }
func (discard) Forget(string) error                               { return nil }

// target is a resolved target: which root, where below it, and how the manifest writes it.
type target struct {
	root    *os.Root
	idx     int
	rel     string // forward slashes, relative to the root
	display string // "~/…" or absolute
}

func (e *Executor) resolve(p string) (target, error) {
	idx, rel, err := e.loc.locate(p)
	if err != nil {
		return target{}, err
	}
	if rel == "" {
		return target{}, fmt.Errorf("%q is a root itself", p)
	}
	return target{root: e.roots[idx], idx: idx, rel: rel, display: e.loc.display(idx, rel)}, nil
}

var toolIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// checkTarget resolves the target of an operation for tool and applies every rule that makes
// it a defect: syntax, prefix, inside a root of the executor and inside a root of the tool.
func (e *Executor) checkTarget(tool Tool, ref resource.Ref, p string) (target, error) {
	defect := func(problem string) error { return &DefectError{Tool: tool.ID, Target: p, Problem: problem} }
	if !toolIDPattern.MatchString(tool.ID) {
		return target{}, defect(fmt.Sprintf("the tool id %q is not lower case with hyphens", tool.ID))
	}
	if !resource.ValidCode(ref.Code) || ref.Version < 0 {
		return target{}, defect(fmt.Sprintf("the operation's resource %q is not a valid reference", ref))
	}
	t, err := e.resolve(p)
	if err != nil {
		return target{}, defect(err.Error())
	}
	if !prefixed(t.rel) {
		return target{}, defect("the name does not carry the prefix " + brand.Prefix)
	}
	for _, r := range tool.Roots {
		idx, rel, err := e.loc.locate(r)
		if err == nil && idx == t.idx && e.loc.under(rel, t.rel) {
			return t, nil
		}
	}
	return target{}, defect("it is not inside any root the tool's detection reported")
}

// step runs the test hook, if any.
func (e *Executor) step(name string) error {
	if e.hook != nil {
		return e.hook(name)
	}
	return nil
}

// Applied is the result of Apply.
type Applied struct {
	// Entry is what was recorded in the manifest.
	Entry manifest.Entry
	// Kept is where the previous content was copied, relative to the local data directory, or
	// "" when nothing was kept.
	Kept string
	// Leftover is a directory moved aside that could not be removed (Windows, a file held
	// open), or "". It carries the prefix and the intent's id; a later run removes it.
	Leftover string
}

// Options change how Apply treats what is at the target.
type Options struct {
	// Keep copies what is at the target to kept/ before it is replaced. The sync engine asks
	// for it when the person overwrites something they changed (--overwrite).
	Keep bool
}

// Apply carries out op for tool. pre is what the plan saw at the target; when the target is no
// longer in that state, nothing is written and the error satisfies ErrChanged.
func (e *Executor) Apply(ctx context.Context, tool Tool, op adapter.Op, pre Observed, opt Options) (Applied, error) {
	if err := ctx.Err(); err != nil {
		return Applied{}, err
	}
	switch op := op.(type) {
	case adapter.WriteTree:
		return e.applyTree(tool, op, pre, opt)
	case adapter.WriteFile:
		return e.applyFile(tool, op, pre, opt)
	case adapter.SetKey:
		return e.applyKey(tool, op, pre, opt)
	case adapter.Exec:
		return Applied{}, fmt.Errorf("running %v: %w", op.Argv, ErrNotImplemented)
	}
	return Applied{}, &DefectError{Tool: tool.ID, Problem: fmt.Sprintf("unknown operation %T", op)}
}

// checkPre compares what is at the target now with what the plan saw.
func checkPre(pre, cur Observed) error {
	if pre.State == Unknown {
		return fmt.Errorf("%w: the plan could not read it", ErrUnreadable)
	}
	if cur.State == Unknown {
		return fmt.Errorf("%w: %v", ErrUnreadable, cur.Err)
	}
	if !pre.Same(cur) {
		return ErrChanged
	}
	return nil
}

func (e *Executor) newEntry(tool Tool, ref resource.Ref, op manifest.Op, t target, digest string) manifest.Entry {
	return manifest.Entry{
		ID:       manifest.EntryID(tool.ID, op, t.display, nil),
		Tool:     tool.ID,
		Resource: ref.Code,
		Version:  ref.Version,
		Op:       op,
		Path:     t.display,
		Digest:   digest,
		At:       e.now().UTC(),
	}
}

// missingParents returns the directories between the root and the target's parent that do not
// exist yet, outermost first, relative to the root.
func missingParents(t target) ([]string, error) {
	var missing []string
	for dir := parentOf(t.rel); dir != ""; dir = parentOf(dir) {
		fi, err := t.root.Lstat(filepath.FromSlash(dir))
		if err == nil {
			if !fi.IsDir() {
				return nil, fmt.Errorf("%s is not a directory", dir)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		missing = append([]string{dir}, missing...)
	}
	return missing, nil
}

func parentOf(rel string) string {
	i := len(rel) - 1
	for i >= 0 && rel[i] != '/' {
		i--
	}
	if i <= 0 {
		return ""
	}
	return rel[:i]
}

// makeParents creates the missing parents and returns them in manifest form.
func (e *Executor) makeParents(t target, missing []string) ([]string, error) {
	var made []string
	for _, dir := range missing {
		if err := t.root.Mkdir(filepath.FromSlash(dir), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			e.removeEmpty(t.root, missing)
			return nil, err
		}
		made = append(made, e.loc.display(t.idx, dir))
	}
	return made, nil
}

// removeEmpty removes directories, innermost first, as long as they are empty.
func (e *Executor) removeEmpty(root *os.Root, dirs []string) {
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := root.Remove(filepath.FromSlash(dirs[i])); err != nil {
			return // not empty, or gone: either way the ones above stay too
		}
	}
}

// createdRel turns the Created list of an entry back into paths relative to the target's root,
// keeping only directories that really are above the target: a manifest edited by hand cannot
// make Revert remove anything else.
func (e *Executor) createdRel(t target, created []string) []string {
	var out []string
	for _, c := range created {
		idx, rel, err := e.loc.locate(c)
		if err == nil && idx == t.idx && rel != "" && e.loc.under(rel, t.rel) {
			out = append(out, rel)
		}
	}
	return out
}

// sibling returns the name next to the target that an interrupted write with the given intent
// id leaves behind: kind is "tmp" for the new content and "old" for the content moved aside.
func sibling(rel, kind, id string) string {
	return rel + "." + kind + "-" + id
}
