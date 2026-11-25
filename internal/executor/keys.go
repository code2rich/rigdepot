// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/configedit"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// Setting a key in another tool's configuration file is the most dangerous thing the executor
// does: the file belongs to the tool and the person, and the tool may rewrite it at any moment.
// Apply follows the steps of docs/architecture/04a-platforms.md ("原子替换与文件锁"):
//
//  1. The caller holds the manifest lock: the Journal is a *manifest.Locked.
//  2. Read the file and its digest. The key must still be as the plan saw it (ErrChanged).
//  3. Parse it. A file that does not parse is unknown and is not written (ErrUnreadable).
//  4. Edit it in memory with package configedit, which checks that the result parses and differs
//     from the file in exactly that key.
//  5. Copy the file to backup/<time>/ in the local data directory.
//  6. Write the result to a temporary file next to it, with the file's permissions, flushed.
//  7. Read the file's digest again. If it changed since step 2 (the tool rewrote it just now),
//     the temporary file is removed and nothing is written (ErrChanged).
//  8. Rename the temporary file over the file.
//
// The intent is logged between steps 4 and 5, and the entry recorded once the key reads back as
// written.

// stepFileChecked is the step after the file's digest was compared again (step 7), right
// before the rename; tests use it to change the file at the worst moment.
const stepFileChecked = "file-checked"

// ErrNotRegularFile: the configuration file is a symbolic link or not a file at all. Replacing it
// would turn a link the person made into a plain file, so it is left alone.
var ErrNotRegularFile = errors.New("the configuration file is not a regular file")

// KeyDigest returns the digest the manifest records for the value of a key: the SHA-256 of the
// value as JSON (encoding/json: object keys sorted, no spaces), normalised the way configedit
// reads values back, so that the value an operation sets and the value read back from the file
// give the same digest.
func KeyDigest(v any) string {
	data, err := json.Marshal(configedit.Normalize(v))
	if err != nil {
		return "other:unencodable " + err.Error() // never equal to a digest that was written
	}
	return fileDigest(data)
}

// keyOwned reports whether the last element of a key is ours: it carries the prefix, or it is
// the product's single MCP entry.
func keyOwned(key []string) bool {
	if len(key) == 0 || slices.Contains(key, "") {
		return false
	}
	last := key[len(key)-1]
	return last == brand.MCPEntry || (len(last) > len(brand.Prefix) && strings.HasPrefix(last, brand.Prefix))
}

// checkKeyTarget is checkTarget for a key: the prefix rule applies to the key instead of the file
// name, and the file may be exactly a place the tool declared (a configuration file the
// profile names, such as ~/.claude.json) as well as lie inside one.
func (e *Executor) checkKeyTarget(tool Tool, op adapter.SetKey) (target, error) {
	defect := func(problem string) error { return &DefectError{Tool: tool.ID, Target: op.File, Problem: problem} }
	if !toolIDPattern.MatchString(tool.ID) {
		return target{}, defect(fmt.Sprintf("the tool id %q is not lower case with hyphens", tool.ID))
	}
	if !resource.ValidCode(op.Resource.Code) || op.Resource.Version < 0 {
		return target{}, defect(fmt.Sprintf("the operation's resource %q is not a valid reference", op.Resource))
	}
	if !op.Format.Valid() {
		return target{}, defect(fmt.Sprintf("unknown configuration format %q", op.Format))
	}
	if !keyOwned(op.Path) {
		return target{}, defect(fmt.Sprintf("the key %q does not end in a name with the prefix %s or the entry %s", op.Path, brand.Prefix, brand.MCPEntry))
	}
	t, err := e.resolve(op.File)
	if err != nil {
		return target{}, defect(err.Error())
	}
	for _, r := range tool.Roots {
		idx, rel, err := e.loc.locate(r)
		if err == nil && idx == t.idx && (e.loc.under(rel, t.rel) || e.loc.same(rel, t.rel)) {
			return t, nil
		}
	}
	return target{}, defect("it is neither a place the tool's profile names nor inside a root its detection reported")
}

// configFile is a configuration file as read: its content and what Observe makes of the key.
type configFile struct {
	exists bool
	data   []byte
	digest string      // of the whole file; "" when it does not exist
	perm   fs.FileMode // permission bits, 0644 for a file that does not exist yet
	key    Observed
}

// readConfig reads a configuration file and looks at one key in it.
func readConfig(t target, format adapter.Format, key []string) configFile {
	name := filepath.FromSlash(t.rel)
	fi, err := t.root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return configFile{perm: 0o644, key: Observed{State: Absent}}
	}
	if err != nil {
		return configFile{key: Observed{Err: err}}
	}
	if !fi.Mode().IsRegular() {
		return configFile{key: Observed{Err: fmt.Errorf("%s: %w (%s)", t.display, ErrNotRegularFile, fi.Mode().Type())}}
	}
	data, err := t.root.ReadFile(name)
	if err != nil {
		return configFile{key: Observed{Err: err}}
	}
	cf := configFile{exists: true, data: data, digest: fileDigest(data), perm: fi.Mode().Perm()}
	v, ok, err := configedit.Get(format, data, key)
	switch {
	case err != nil:
		cf.key = Observed{Err: fmt.Errorf("%s: %w", t.display, err)}
	case !ok:
		cf.key = Observed{State: Absent}
	default:
		cf.key = Observed{State: Present, Digest: KeyDigest(v)}
	}
	return cf
}

// fileNow returns the digest of the file as it is now, "" when it does not exist.
func fileNow(t target) (string, error) {
	data, err := t.root.ReadFile(filepath.FromSlash(t.rel))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return fileDigest(data), nil
}

func (e *Executor) observeKey(tt Target) Observed {
	if !tt.Format.Valid() {
		return Observed{Err: fmt.Errorf("unknown configuration format %q", tt.Format)}
	}
	t, err := e.resolve(tt.Path)
	if err != nil {
		return Observed{Err: err}
	}
	return readConfig(t, tt.Format, tt.Key).key
}

func (e *Executor) applyKey(tool Tool, op adapter.SetKey, pre Observed, opt Options) (Applied, error) {
	t, err := e.checkKeyTarget(tool, op)
	if err != nil {
		return Applied{}, err
	}
	// Steps 2 and 3: read, compare with the plan, parse.
	cf := readConfig(t, op.Format, op.Path)
	if err := checkPre(pre, cf.key); err != nil {
		return Applied{}, fmt.Errorf("%s %q: %w", t.display, op.Path, err)
	}
	// Step 4: edit in memory; configedit checks its own result.
	out, edit, err := configedit.Set(op.Format, cf.data, op.Path, op.Value)
	if err != nil {
		if errors.Is(err, configedit.ErrInvalid) {
			return Applied{}, &DefectError{Tool: tool.ID, Target: op.File, Problem: err.Error()}
		}
		return Applied{}, fmt.Errorf("%s %q: %w", t.display, op.Path, err)
	}

	entry := e.newEntry(tool, op.Resource, manifest.OpKey, t, KeyDigest(op.Value))
	entry.ID = manifest.EntryID(tool.ID, manifest.OpKey, t.display, op.Path)
	entry.Format = string(op.Format)
	entry.Key = slices.Clone(op.Path)
	if edit.Created > 0 {
		// An edit that replaced a value is not recorded: undoing an entry removes the key, and
		// putting a previous value back is not that.
		entry.Edit = &manifest.KeyEdit{Offset: edit.Offset, Old: edit.Old, New: edit.New, Created: edit.Created}
	}
	var missing []string
	if !cf.exists {
		if missing, err = missingParents(t); err != nil {
			return Applied{}, fmt.Errorf("%s: %w", t.display, err)
		}
		for _, dir := range missing {
			entry.Created = append(entry.Created, e.loc.display(t.idx, dir))
		}
		entry.Created = append(entry.Created, t.display) // the file itself
	}
	// Step 5: back up the file as it was. Before the intent, so that the intent names it.
	if cf.exists {
		if entry.Backup, err = e.backup(t, cf.data); err != nil {
			return Applied{}, fmt.Errorf("backing up %s: %w", t.display, err)
		}
	}
	in, err := e.journal.Intend(manifest.Intent{Kind: manifest.Write, Entry: entry, Before: before(cf.key), After: entry.Digest})
	if err != nil {
		return Applied{}, err
	}
	fail := func(cause error) (Applied, error) {
		removeAllIn(t.root, sibling(t.rel, "tmp", in.ID))
		e.removeEmpty(t.root, missing)
		return Applied{}, e.forget(in, cause)
	}
	var res Applied
	if opt.Keep && cf.key.State == Present {
		if res.Kept, err = e.keepKey(t, op.Format, op.Path, cf.data); err != nil {
			return fail(fmt.Errorf("keeping %s %q before replacing it: %w", t.display, op.Path, err))
		}
	}
	if _, err := e.makeParents(t, missing); err != nil {
		return fail(err)
	}

	// Steps 6 to 8.
	if renamed, err := e.replaceFile(t, in.ID, cf, out); err != nil {
		if renamed {
			// The file has its new content: the intent stays, recovery finds it done.
			return Applied{}, fmt.Errorf("%w (the new content of %s is in place)", err, t.display)
		}
		return fail(err)
	}

	// Read back: the key has to be what was asked for.
	if got := readConfig(t, op.Format, op.Path).key; got.State != Present || got.Digest != entry.Digest {
		return Applied{}, fmt.Errorf("%s %q: %w", t.display, op.Path, ErrVerify)
	}
	recorded, err := e.journal.Commit(in)
	if err != nil {
		return Applied{}, err
	}
	res.Entry = recorded
	return res, nil
}

// replaceFile writes data over the configuration file read as cf: steps 6 to 8. It fails with
// ErrChanged, writing nothing, when the file changed since it was read. renamed reports whether
// the file has the new content, which a failure after the rename leaves it with. On a failure
// before the rename the temporary file may be left for the caller to remove.
func (e *Executor) replaceFile(t target, id string, cf configFile, data []byte) (renamed bool, err error) {
	tmp := sibling(t.rel, "tmp", id)
	if err := writeNew(t.root, tmp, data, cf.perm); err != nil {
		return false, err
	}
	// The permissions of the original, whatever the umask made of them.
	if err := t.root.Chmod(filepath.FromSlash(tmp), cf.perm); err != nil {
		return false, err
	}
	if err := e.step(stepStaged); err != nil {
		return false, err
	}
	now, err := fileNow(t)
	if err != nil {
		return false, fmt.Errorf("%s: %w: %v", t.display, ErrUnreadable, err)
	}
	if now != cf.digest {
		return false, fmt.Errorf("%s: %w (it was rewritten while it was being edited)", t.display, ErrChanged)
	}
	if err := e.step(stepFileChecked); err != nil {
		return false, err
	}
	if err := renameIn(t.root, tmp, t.rel); err != nil {
		return false, err
	}
	home.SyncDir(filepath.Join(e.rootPath(t.idx), filepath.FromSlash(parentOf(t.rel))))
	return true, e.step(stepMovedNew)
}

// rootPath returns the absolute path of a root on this system.
func (e *Executor) rootPath(idx int) string { return e.roots[idx].Name() }

// backup copies a configuration file to backup/<time>/ in the local data directory, at its path
// inside the home directory, and returns where, relative to the local data directory. All
// backups of one executor go to one directory; when a file is changed several times, the first
// copy, the file as it was before this run touched it, is the one that stays. A preview without
// a local data directory makes no backup.
func (e *Executor) backup(t target, data []byte) (string, error) {
	if e.data.Path() == "" {
		return "", nil
	}
	if e.backupAbs == "" {
		abs, rel, err := e.data.NewStamped(home.BackupDir, e.now())
		if err != nil {
			return "", err
		}
		e.backupAbs, e.backupRel = abs, rel
	}
	dest := filepath.Join(e.backupAbs, keptPath(t.display))
	if err := os.MkdirAll(filepath.Dir(dest), home.DirPerm); err != nil {
		return "", err
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, home.FilePerm)
	switch {
	case errors.Is(err, fs.ErrExist):
		// Backed up already in this run, before an earlier key was set.
	case err != nil:
		return "", err
	default:
		if _, err := f.Write(data); err != nil {
			f.Close()
			return "", err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
	}
	return e.backupRel + "/" + filepath.ToSlash(keptPath(t.display)), nil
}

// keepKey copies the value of a key to kept/<time>/, next to where the file would be kept, as
// "<file>#<key>.json", and returns where, relative to the local data directory.
func (e *Executor) keepKey(t target, format adapter.Format, key []string, data []byte) (string, error) {
	v, ok, err := configedit.Get(format, data, key)
	if err != nil || !ok {
		return "", fmt.Errorf("reading the value to keep: %v", err)
	}
	text, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	if e.data.Path() == "" {
		return "", errors.New("there is no local data directory to keep it in")
	}
	if e.keptAbs == "" {
		abs, rel, err := e.data.NewStamped(home.KeptDir, e.now())
		if err != nil {
			return "", err
		}
		e.keptAbs, e.keptRel = abs, rel
	}
	name := keptPath(t.display) + "#" + strings.Join(key, ".") + ".json"
	dest := filepath.Join(e.keptAbs, name)
	if err := os.MkdirAll(filepath.Dir(dest), home.DirPerm); err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, append(text, '\n'), home.FilePerm); err != nil {
		return "", err
	}
	return e.keptRel + "/" + filepath.ToSlash(name), nil
}

// revertKey removes a key we set. While the bytes we wrote are still in place they are taken out
// exactly, so that the file is as it was before; when the file was rewritten since (a tool
// reformats its file), the key is removed by its meaning. A value changed since is kept first.
// The file goes too when we created it and nothing else is in it.
func (e *Executor) revertKey(en manifest.Entry) (Reverted, error) {
	format := adapter.Format(en.Format)
	if !format.Valid() {
		return Reverted{}, fmt.Errorf("entry %s: unknown configuration format %q", en.ID, en.Format)
	}
	if !keyOwned(en.Key) {
		return Reverted{}, fmt.Errorf("entry %s: the key %q is not ours; refusing to remove it", en.ID, en.Key)
	}
	t, err := e.resolve(en.Path)
	if err != nil {
		return Reverted{}, fmt.Errorf("entry %s: %w", en.ID, err)
	}
	cf := readConfig(t, format, en.Key)
	if cf.key.State == Unknown {
		return Reverted{}, fmt.Errorf("%s %q: %w: %v", t.display, en.Key, ErrUnreadable, cf.key.Err)
	}
	var res Reverted
	var out []byte
	switch {
	case cf.key.State == Absent:
		res.Outcome = AlreadyGone
	default:
		res.Outcome = Removed
		if cf.key.Digest != en.Digest {
			res.Outcome = RemovedKept
			if res.Kept, err = e.keepKey(t, format, en.Key, cf.data); err != nil {
				return Reverted{}, fmt.Errorf("keeping the changed %s %q: %w", t.display, en.Key, err)
			}
		}
		if en.Edit != nil {
			out, _, err = configedit.Revert(cf.data, configedit.Edit{
				Format: format, Path: en.Key, Offset: en.Edit.Offset, Old: en.Edit.Old, New: en.Edit.New, Created: en.Edit.Created,
			})
		} else {
			out, err = configedit.Delete(format, cf.data, en.Key)
		}
		if err != nil {
			return Reverted{}, fmt.Errorf("%s %q: %w", t.display, en.Key, err)
		}
	}
	in, err := e.journal.Intend(manifest.Intent{Kind: manifest.Remove, Entry: en, Before: before(cf.key), After: ""})
	if err != nil {
		return Reverted{}, err
	}
	switch {
	case cf.key.State == Absent:
	case slices.Contains(en.Created, en.Path) && onlyEmpty(format, out):
		// The file was ours and held nothing else: remove it, unless it changed meanwhile.
		if now, err := fileNow(t); err != nil || now != cf.digest {
			return Reverted{}, e.forget(in, fmt.Errorf("%s: %w", t.display, ErrChanged))
		}
		if err := t.root.Remove(filepath.FromSlash(t.rel)); err != nil {
			return Reverted{}, e.forget(in, err)
		}
	default:
		if renamed, err := e.replaceFile(t, in.ID, cf, out); err != nil {
			if renamed {
				return Reverted{}, fmt.Errorf("%w (the key is already out of %s)", err, t.display)
			}
			removeAllIn(t.root, sibling(t.rel, "tmp", in.ID))
			return Reverted{}, e.forget(in, err)
		}
	}
	if _, err := t.root.Lstat(filepath.FromSlash(t.rel)); errors.Is(err, fs.ErrNotExist) {
		e.removeEmpty(t.root, e.createdRel(t, en.Created))
	}
	if _, err := e.journal.Commit(in); err != nil {
		return Reverted{}, err
	}
	return res, nil
}

// onlyEmpty reports whether what is left of a file we created is nothing: no bytes, white space,
// or for JSON an empty object.
func onlyEmpty(format adapter.Format, out []byte) bool {
	rest := strings.Map(func(r rune) rune {
		if strings.ContainsRune(" \t\r\n\ufeff", r) {
			return -1
		}
		return r
	}, string(out))
	return rest == "" || (format != adapter.TOML && rest == "{}")
}

// settleKey is Settle for a key: an interrupted edit leaves at most its temporary file, which is
// removed. The rename either happened or it did not, so the file is in one state or the other.
func (e *Executor) settleKey(in manifest.Intent) manifest.Observation {
	en := in.Entry
	format := adapter.Format(en.Format)
	if !format.Valid() || !keyOwned(en.Key) {
		return manifest.Observation{Err: fmt.Errorf("entry %s: not a key of ours in a known format", en.ID)}
	}
	t, err := e.resolve(en.Path)
	if err != nil {
		return manifest.Observation{Err: err}
	}
	removeAllIn(t.root, sibling(t.rel, "tmp", in.ID))
	o := readConfig(t, format, en.Key).key
	switch o.State {
	case Unknown:
		return manifest.Observation{Err: o.Err}
	case Absent:
		if _, err := t.root.Lstat(filepath.FromSlash(t.rel)); errors.Is(err, fs.ErrNotExist) {
			e.removeEmpty(t.root, e.createdRel(t, en.Created))
		}
		return manifest.Observation{}
	}
	return manifest.Observation{Exists: true, Digest: o.Digest}
}
