// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/executor"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/localconfig"
	"github.com/code2rich/rigdepot/internal/manifest"
	"github.com/code2rich/rigdepot/internal/sandbox"
	"github.com/code2rich/rigdepot/internal/sync"
)

func init() { register("", newStatusCmd) }

type statusFlags struct {
	src      sourceFlags
	diff     bool
	exitCode bool
}

// newStatusCmd: rgd status [code]. What was written, where, whether it is still as written
// (drift), and how it compares with the resource repository. It changes nothing and works when
// the repository cannot be reached: then it only compares the disk with the manifest.
//
// Exit codes: 9 when an entry cannot be read (its state is unknown) or the manifest cannot be
// read; 3 when something was changed or deleted on this computer; with --exit-code, 5 when a
// sync would change anything; 0 otherwise.
func newStatusCmd(a *app) *cobra.Command {
	var f statusFlags
	cmd := &cobra.Command{
		Use:   "status [resource]",
		Short: a.p.T(msg.StatusShort),
		Args:  a.maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code := ""
			if len(args) == 1 {
				code = args[0]
			}
			return a.runStatus(cmd.Context(), f, code)
		},
	}
	a.addSourceFlags(cmd, &f.src)
	cmd.Flags().BoolVar(&f.diff, "diff", false, a.p.T(msg.FlagDiff))
	cmd.Flags().BoolVar(&f.exitCode, "exit-code", false, a.p.T(msg.FlagStatusExitCode))
	return cmd
}

// maxArgs accepts at most n arguments.
func (a *app) maxArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > n {
			return a.usageError("argument_count",
				a.p.T(msg.ErrorMaxArgs, i18n.Count(n), i18n.V("command", cmd.CommandPath()), i18n.V("got", len(args))), cmd.CommandPath())
		}
		return nil
	}
}

func (a *app) runStatus(ctx context.Context, f statusFlags, code string) error {
	l, err := a.local()
	if err != nil {
		return err
	}
	// The repository is only needed to say what a sync would change. Without it, status still
	// compares the disk with the manifest.
	st, rem, err := a.resolveSource(ctx, l, f.src)
	var sourceErr string
	switch {
	case errors.Is(err, errNoSource):
		st = nil
	case err != nil:
		var ce *cliout.Error
		if !errors.As(err, &ce) {
			return err
		}
		st, sourceErr = nil, ce.Message
		a.out.Warn(a.p.T(msg.StatusSourceFailed, i18n.V("detail", ce.Message)))
	}
	opt := sync.Options{Disabled: l.cfg.List(localconfig.KeyToolsDisabled), MCPMode: adapter.MCPNativeEnv}
	rem.apply(&opt)
	s, err := l.eng.Status(ctx, st, opt)
	if err != nil {
		return a.stateError(err)
	}
	if code != "" {
		known := st != nil && st.Known(code)
		s.Entries = slices.DeleteFunc(s.Entries, func(e sync.StatusEntry) bool {
			if e.Entry.Resource == code {
				known = true
			}
			return e.Entry.Resource != code
		})
		s.Pending = slices.DeleteFunc(s.Pending, func(it sync.Item) bool { return it.Resource.Code != code })
		if !known {
			return cliout.Fail(cliout.ExitUsage, "no_such_resource", a.p.T(msg.StatusNoSuchResource, i18n.V("resource", code)))
		}
	}
	doc := a.newStatusDoc(l, s, st, sourceErr, f.diff)
	if err := a.out.Result(cliout.Schema("status", 1), doc, func(w io.Writer) error {
		return a.writeStatus(w, l, s, st, doc, f, code)
	}); err != nil {
		return err
	}
	return statusExit(s, f.exitCode)
}

func statusExit(s *sync.Status, exitCode bool) error {
	has := func(st sync.EntryState) bool {
		return slices.ContainsFunc(s.Entries, func(e sync.StatusEntry) bool { return e.State == st })
	}
	switch {
	case has(sync.Unreadable):
		return cliout.Status(cliout.ExitStateUnknown)
	case has(sync.Modified), has(sync.Deleted):
		return cliout.Status(cliout.ExitPending)
	case exitCode && s.Differs():
		return cliout.Status(cliout.ExitDiff)
	}
	return nil
}

// statusDoc is the JSON output of rgd status (schema rgd.status/v1).
type statusDoc struct {
	Source      *sourceDoc       `json:"source,omitempty"`
	SourceError string           `json:"sourceError,omitempty"`
	Tools       []toolDoc        `json:"tools,omitempty"`
	Entries     []statusEntryDoc `json:"entries"`
	Pending     []opDoc          `json:"pending"`
	Failed      []failedDoc      `json:"failed"`
	Kept        []keptDoc        `json:"kept"`
	Summary     map[string]int   `json:"summary"`
}

type statusEntryDoc struct {
	ID       string   `json:"id"`
	Tool     string   `json:"tool"`
	Resource string   `json:"resource"`
	Version  int      `json:"version,omitempty"`
	Kind     string   `json:"kind"`
	Path     string   `json:"path"`
	Key      []string `json:"key,omitempty"`
	State    string   `json:"state"`
	At       string   `json:"at"`
	Detail   string   `json:"detail,omitempty"`
	Diff     *diffDoc `json:"diff,omitempty"`
}

// diffDoc compares what is on this computer with what the repository wants. Available is false
// when there is nothing to compare with (the repository was not read, or the target is a key).
type diffDoc struct {
	Available bool          `json:"available"`
	Files     []fileDiffDoc `json:"files"`
}

type fileDiffDoc struct {
	Path string `json:"path"`
	// Change is "modified", "added" (only on this computer) or "removed" (only in the
	// repository).
	Change string    `json:"change"`
	Binary bool      `json:"binary,omitempty"`
	Lines  []lineDoc `json:"lines,omitempty"`
}

type lineDoc struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// keptDoc is one directory below kept/: the versions of files people had changed, copied there
// before an overwrite or an uninstall.
type keptDoc struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
}

func (a *app) newStatusDoc(l *local, s *sync.Status, st *desired.State, sourceErr string, diff bool) statusDoc {
	doc := statusDoc{SourceError: sourceErr, Entries: []statusEntryDoc{}, Pending: []opDoc{}, Failed: []failedDoc{}, Kept: a.keptDirs(l), Summary: map[string]int{}}
	if st != nil {
		doc.Source = newSourceDoc(st.Source)
		doc.Failed = a.newPlanDoc(&sync.Plan{}, st, nil).Failed
	}
	for _, t := range s.Tools {
		doc.Tools = append(doc.Tools, toolDoc{ID: t.ID, Name: t.Name, Status: string(t.Status), Version: t.Detection.Version, Reason: t.Reason, Held: t.Held})
	}
	for _, state := range []sync.EntryState{sync.Synced, sync.Outdated, sync.Modified, sync.Deleted, sync.Unreadable, sync.Removing} {
		doc.Summary[string(state)] = 0
	}
	for _, e := range s.Entries {
		d := statusEntryDoc{ID: e.Entry.ID, Tool: e.Entry.Tool, Resource: e.Entry.Resource, Version: e.Entry.Version,
			Kind: string(e.Entry.Op), Path: e.Entry.Path, Key: e.Entry.Key, State: string(e.State), At: e.Entry.At.UTC().Format("2006-01-02T15:04:05Z")}
		if e.State == sync.Unreadable && e.Observed.Err != nil {
			d.Detail = e.Observed.Err.Error()
		}
		if diff && (e.State == sync.Modified || e.State == sync.Outdated) {
			d.Diff = a.entryDiff(l, e)
		}
		doc.Entries = append(doc.Entries, d)
		doc.Summary[string(e.State)]++
	}
	for _, it := range s.Pending {
		doc.Pending = append(doc.Pending, newOpDoc(it, false))
	}
	doc.Summary["pending"] = len(s.Pending)
	return doc
}

// keptDirs lists what is in kept/, newest last.
func (a *app) keptDirs(l *local) []keptDoc {
	out := []keptDoc{}
	root := l.data.Join(home.KeptDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n := 0
		_ = filepath.WalkDir(filepath.Join(root, e.Name()), func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				n++
			}
			return nil
		})
		out = append(out, keptDoc{Path: home.KeptDir + "/" + e.Name(), Files: n})
	}
	return out
}

// entryDiff compares the target of an entry with what the repository wants there.
func (a *app) entryDiff(l *local, e sync.StatusEntry) *diffDoc {
	d := &diffDoc{Files: []fileDiffDoc{}}
	want := map[string][]byte{}
	switch op := e.Want.(type) {
	case adapter.WriteTree:
		for _, f := range op.Files {
			if !underLocal(f.Path, op.Local) {
				want[f.Path] = f.Data
			}
		}
	case adapter.WriteFile:
		want[""] = op.Data
	default:
		return d
	}
	d.Available = true
	full := a.nativePath(l, e.Entry.Path)
	have := map[string][]byte{}
	if e.Entry.Op == manifest.OpFile {
		if data, err := os.ReadFile(full); err == nil {
			have[""] = data
		}
	} else {
		fsys := os.DirFS(full)
		_ = fs.WalkDir(fsys, ".", func(p string, de fs.DirEntry, err error) error {
			if err != nil || p == executor.MarkerFile {
				return nil
			}
			if underLocal(p, e.Entry.Local) {
				// What is in a local directory belongs to this computer, not to the resource.
				if de.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if de.IsDir() {
				return nil
			}
			if data, err := fs.ReadFile(fsys, p); err == nil {
				have[p] = data
			}
			return nil
		})
	}
	var paths []string
	for p := range want {
		paths = append(paths, p)
	}
	for p := range have {
		if _, ok := want[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	for _, p := range paths {
		w, inWant := want[p]
		h, inHave := have[p]
		fd := fileDiffDoc{Path: p}
		switch {
		case inWant && !inHave:
			fd.Change = "removed"
		case !inWant && inHave:
			fd.Change = "added"
		case string(w) == string(h):
			continue
		default:
			fd.Change = "modified"
		}
		lines, ok := sandbox.LineDiff(w, h)
		if !ok {
			fd.Binary = true
		}
		for _, ln := range lines {
			if ln.Op != sandbox.Same {
				fd.Lines = append(fd.Lines, lineDoc{Op: string(ln.Op), Text: ln.Text})
			}
		}
		d.Files = append(d.Files, fd)
	}
	return d
}

// underLocal reports whether the slash-separated path p is one of the local directories of a
// tree, or lies below one.
func underLocal(p string, local []string) bool {
	for _, l := range local {
		if p == l || strings.HasPrefix(p, l+"/") {
			return true
		}
	}
	return false
}

// nativePath is where a target ("~/…" or absolute) is on this computer.
func (a *app) nativePath(l *local, p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return filepath.Join(l.userHome, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/")))
	}
	return filepath.FromSlash(p)
}

// maxDiffLines bounds the lines of one file shown by status --diff; --json has all of them.
const maxDiffLines = 40

// writeStatus writes the human output of rgd status: one group per state, the entries that need
// the person first.
func (a *app) writeStatus(w io.Writer, l *local, s *sync.Status, st *desired.State, doc statusDoc, f statusFlags, code string) error {
	var v view
	switch {
	case st != nil:
		src := st.Source.URL
		if st.Source.Commit != "" {
			src += " @ " + st.Source.Commit[:min(12, len(st.Source.Commit))]
		}
		key := msg.StatusSource
		if st.Source.Type == desired.SourceServer {
			key = msg.StatusServer
		}
		v.head = append(v.head, a.out.Paint(cliout.Dim, a.p.T(key, i18n.V("source", src))))
	case doc.SourceError == "":
		v.head = append(v.head, a.out.Paint(cliout.Dim, a.p.T(msg.StatusNoSource)))
	default:
		v.head = append(v.head, a.out.Paint(cliout.Dim, a.p.T(msg.StatusSourceUnavailable)))
	}
	if len(s.Entries) == 0 {
		v.head = append(v.head, a.p.T(msg.StatusEmpty))
	} else {
		v.head = append(v.head, a.p.T(msg.StatusTotal, i18n.Count(len(s.Entries)), i18n.V("synced", doc.Summary[string(sync.Synced)])))
	}
	names := map[string]string{}
	for _, t := range s.Tools {
		names[t.ID] = t.Name
	}
	for _, ad := range a.adapters() {
		if names[ad.ID()] == "" {
			names[ad.ID()] = ad.Profile().Name
		}
	}
	toolName := func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return id
	}
	groups := []struct {
		state sync.EntryState
		title string
		mark  string
		style cliout.Style
		warn  bool
	}{
		{sync.Modified, msg.StatusGroupModified, "!", cliout.Yellow, true},
		{sync.Deleted, msg.StatusGroupDeleted, "!", cliout.Yellow, true},
		{sync.Unreadable, msg.StatusGroupUnreadable, "?", cliout.Red, true},
		{sync.Outdated, msg.StatusGroupOutdated, "~", cliout.Cyan, false},
		{sync.Removing, msg.StatusGroupRemoving, "-", cliout.Red, false},
		{sync.Synced, msg.StatusGroupSynced, "=", cliout.Green, false},
	}
	var diffs []string
	for _, g := range groups {
		grp := group{title: a.p.T(g.title), warn: g.warn}
		for i, e := range s.Entries {
			if e.State != g.state {
				continue
			}
			path := a.showTarget(l.userHome, e.Entry.Path)
			if e.Entry.Op == manifest.OpTree {
				path += "/"
			}
			note := e.Entry.Resource + " · " + toolName(e.Entry.Tool)
			if len(e.Entry.Key) > 0 {
				note = strings.Join(e.Entry.Key, ".") + " · " + note
			}
			cmd := brand.CLI + " sync --overwrite " + e.Entry.Resource
			switch e.State {
			case sync.Modified:
				note += " · " + a.p.T(msg.StatusHintModified, i18n.V("command", cmd))
			case sync.Deleted:
				note += " · " + a.p.T(msg.StatusHintDeleted, i18n.V("command", cmd))
			case sync.Unreadable:
				note += " · " + doc.Entries[i].Detail
			}
			grp.lines = append(grp.lines, line{mark: g.mark, style: g.style, path: path, note: note})
			if d := doc.Entries[i].Diff; d != nil {
				diffs = append(diffs, a.diffText(path, d)...)
			}
		}
		v.add(grp)
	}
	pending := group{title: a.p.T(msg.StatusGroupPending)}
	taken := group{title: a.p.T(msg.PlanGroupAttention), warn: true}
	for _, it := range s.Pending {
		path := a.targetPath(l, it)
		note := it.Resource.Code + " · " + toolName(it.Tool)
		if it.Action == sync.ActionTaken {
			taken.lines = append(taken.lines, line{mark: "!", style: cliout.Yellow, path: path, note: note + " · " + a.p.T(msg.PlanNoteTaken)})
			continue
		}
		pending.lines = append(pending.lines, line{mark: "+", style: cliout.Green, path: path, note: note})
	}
	v.add(pending)
	v.add(taken)
	if st != nil {
		failed := group{title: a.p.T(msg.PlanGroupSkipped), warn: true}
		for _, fl := range st.Failed {
			if code != "" && fl.Ref.Code != code {
				continue
			}
			var issues []string
			for _, is := range fl.Issues {
				if !slices.Contains(issues, is.Code) {
					issues = append(issues, is.Code)
				}
			}
			failed.lines = append(failed.lines, line{mark: "x", style: cliout.Yellow, path: fl.Path,
				note: a.p.T(msg.PlanResourceFailed, i18n.V("issues", strings.Join(issues, ", ")))})
		}
		v.add(failed)
	}
	if len(doc.Kept) > 0 {
		kept := group{title: a.p.T(msg.StatusGroupKept)}
		for _, k := range doc.Kept {
			kept.lines = append(kept.lines, line{mark: " ", path: a.showData(l, strings.Split(k.Path, "/")...) + "/",
				note: a.p.T(msg.PlanNoteFiles, i18n.Count(k.Files))})
		}
		v.add(kept)
	}
	if f.diff && len(diffs) == 0 {
		v.foot = append(v.foot, a.p.T(msg.StatusNoDiff))
	}
	if s.Differs() {
		v.foot = append(v.foot, a.p.T(msg.StatusSyncHint, i18n.V("command", commandLine("sync --plan", f.src))))
	}
	if err := a.writeView(w, v); err != nil {
		return err
	}
	if len(diffs) > 0 {
		_, err := io.WriteString(w, "\n"+strings.Join(diffs, "\n")+"\n")
		return err
	}
	return nil
}

// diffText is the human form of one entry's difference: per file a heading, then the lines that
// differ, "-" as the repository has them and "+" as they are on this computer.
func (a *app) diffText(path string, d *diffDoc) []string {
	out := []string{"  " + a.out.Paint(cliout.Bold, a.p.T(msg.StatusDiffHeading, i18n.V("path", path)))}
	if !d.Available {
		return append(out, "    "+a.p.T(msg.StatusDiffUnavailable))
	}
	for _, f := range d.Files {
		name := f.Path
		if name == "" {
			name = path
		}
		var key string
		switch f.Change {
		case "added":
			key = msg.StatusDiffAdded
		case "removed":
			key = msg.StatusDiffRemoved
		default:
			key = msg.StatusDiffModified
		}
		out = append(out, "    "+a.p.T(key, i18n.V("file", name)))
		if f.Binary {
			out = append(out, "      "+a.p.T(msg.StatusDiffBinary))
			continue
		}
		for i, ln := range f.Lines {
			if i == maxDiffLines {
				out = append(out, "      "+a.p.T(msg.StatusDiffMore, i18n.Count(len(f.Lines)-maxDiffLines)))
				break
			}
			style := cliout.Green
			if ln.Op == "-" {
				style = cliout.Red
			}
			out = append(out, "      "+a.out.Paint(style, ln.Op+" "+ln.Text))
		}
	}
	return out
}
