// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/manifest"
	"github.com/code2rich/rigdepot/internal/sandbox"
	"github.com/code2rich/rigdepot/internal/sync"
)

func init() { register("", newUninstallCmd) }

type uninstallFlags struct {
	plan     bool
	tools    []string
	keepHome bool
}

// newUninstallCmd: rgd uninstall. It undoes every write the manifest records, newest first, and
// nothing else; then it deletes the local data directory, unless --keep-home, --tool, or a write
// that could not be undone says otherwise. It needs neither a server nor the repository.
//
// What people changed in a target is copied to kept/ before the target is removed. kept/ is not
// moved out of the local data directory (only the executor writes outside it, rule R7): when it
// holds something, the local data directory is emptied except for kept/, and its path is shown.
//
// Exit codes: 0 done; 3 a target changed while uninstalling and was left; 9 a target or the
// manifest cannot be read (that tool is left as it is), or another rgd holds the lock.
func newUninstallCmd(a *app) *cobra.Command {
	var f uninstallFlags
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: a.p.T(msg.UninstallShort),
		Args:  a.noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runUninstall(cmd.Context(), f)
		},
	}
	fs := cmd.Flags()
	fs.BoolVar(&f.plan, "plan", false, a.p.T(msg.FlagUninstallPlan))
	fs.StringArrayVar(&f.tools, "tool", nil, a.p.T(msg.FlagUninstallTool))
	fs.BoolVar(&f.keepHome, "keep-home", false, a.p.T(msg.FlagKeepHome, i18n.V("dir", "~/"+brand.Home)))
	return cmd
}

func (a *app) runUninstall(ctx context.Context, f uninstallFlags) error {
	l, err := a.local()
	if err != nil {
		return err
	}
	opt := sync.UninstallOptions{Tools: f.tools}
	if f.plan {
		p, err := l.eng.PlanUninstall(ctx, opt)
		if err != nil {
			return a.stateError(err)
		}
		var prev *sandbox.Result
		if len(p.Items) > 0 { // nothing to preview: do not create the local data directory for it
			r, err := l.eng.Preview(ctx, p)
			if err != nil {
				return a.previewError(err)
			}
			prev = &r
		}
		return a.out.Result(cliout.Schema("plan", 1), a.newPlanDoc(p, nil, prev), func(w io.Writer) error {
			return a.writeView(w, a.uninstallPlanView(l, p, f))
		})
	}

	if _, err := os.Stat(l.data.Path()); errors.Is(err, fs.ErrNotExist) {
		// Nothing was ever written: no manifest, nothing to undo, nothing to delete.
		doc := uninstallDoc{Plan: planDoc{Uninstall: true, Tools: []toolDoc{}, Ops: []opDoc{}, Attention: []opDoc{}, Degraded: []degradedDoc{},
			Shared: []sharedDoc{}, Skipped: []skipDoc{}, Failed: []failedDoc{}, Preview: []changeDoc{}}, Done: []doneDoc{}, Retry: []retryDoc{},
			Home: homeDoc{Path: a.showData(l), Removed: true}}
		return a.out.Result(cliout.Schema("uninstall", 1), doc, func(w io.Writer) error {
			return a.writeView(w, view{head: []string{a.p.T(msg.UninstallNothing)}})
		})
	}

	s, err := l.eng.Lock(ctx)
	if err != nil {
		return a.stateError(err)
	}
	p, err := l.eng.PlanUninstall(ctx, opt)
	if err != nil {
		s.Release()
		return a.stateError(err)
	}
	var prev sandbox.Result
	if len(p.Items) > 0 {
		if prev, err = l.eng.Preview(ctx, p); err != nil {
			s.Release()
			return a.previewError(err)
		}
	}
	res, applyErr := s.Apply(ctx, p, p.Approve(sync.ConsentInput{}, time.Now()))
	unresolved := len(s.Recovery.Unresolved)
	s.Release()
	if res == nil {
		return a.stateError(applyErr)
	}

	doc := uninstallDoc{Plan: a.newPlanDoc(p, nil, &prev), Done: []doneDoc{}, Retry: []retryDoc{}, Home: homeDoc{Path: a.showData(l)}}
	for _, d := range res.Done {
		doc.Done = append(doc.Done, doneDoc{opDoc: newOpDoc(d.Item, false), Kept: d.Kept, Outcome: string(d.Outcome), Leftover: d.Leftover})
	}
	for _, r := range res.Retry {
		doc.Retry = append(doc.Retry, retryDoc{opDoc: newOpDoc(r.Item, false), Error: r.Err.Error()})
	}
	unreadable := slices.ContainsFunc(p.Tools, func(t sync.Tool) bool { return t.Status == sync.ToolUnreadable })

	// The local data directory goes only when nothing is left that it is the record of.
	switch {
	case f.keepHome:
		doc.Home.Reason = "keep_home"
	case len(f.tools) > 0:
		doc.Home.Reason = "tool"
	case applyErr != nil || len(res.Retry) > 0 || unreadable || unresolved > 0:
		doc.Home.Reason = "entries_left"
	default:
		m, err := manifest.Open(l.data).Read()
		if err != nil || len(m.Entries) > 0 {
			doc.Home.Reason = "entries_left"
			break
		}
		doc.Home.Removed, doc.Home.Kept, doc.Home.Left = a.removeData(l)
		if !doc.Home.Removed && doc.Home.Kept == "" {
			doc.Home.Reason = "not_empty"
		}
	}

	var failure *cliout.Error
	if applyErr != nil {
		failure = cliout.Fail(cliout.ExitFailure, "undo_failed", a.p.T(msg.UninstallFailed, i18n.V("detail", applyErr.Error())))
		failure.Err = applyErr
		doc.Error = &errorField{Code: failure.Code, Message: failure.Message}
	}
	if err := a.out.Result(cliout.Schema("uninstall", 1), doc, func(w io.Writer) error {
		return a.writeView(w, a.uninstallResultView(l, p, res, doc))
	}); err != nil {
		return err
	}
	switch {
	case failure != nil:
		return failure
	case unreadable:
		return cliout.Status(cliout.ExitStateUnknown)
	case len(res.Retry) > 0:
		return cliout.Status(cliout.ExitPending)
	}
	return nil
}

// uninstallDoc is the JSON output of rgd uninstall (schema rgd.uninstall/v1).
type uninstallDoc struct {
	Plan  planDoc     `json:"plan"`
	Done  []doneDoc   `json:"done"`
	Retry []retryDoc  `json:"retry"`
	Home  homeDoc     `json:"home"`
	Error *errorField `json:"error,omitempty"`
}

// homeDoc says what became of the local data directory.
type homeDoc struct {
	Path    string `json:"path"`
	Removed bool   `json:"removed"`
	// Kept is the kept/ directory that was left in place, holding people's changed files.
	Kept string `json:"kept,omitempty"`
	// Reason says why the directory stays: keep_home, tool, entries_left, not_empty.
	Reason string `json:"reason,omitempty"`
	// Left lists what is still in it that this program does not know.
	Left []string `json:"left,omitempty"`
}

// dataNames are what this program keeps in the local data directory. Uninstall removes these and
// nothing else, so that a --home pointing at the wrong directory cannot cost anybody their files.
var dataNames = []string{
	home.ConfigFile, home.ManifestFile, home.ManifestPrevious, home.JournalFile, home.LockFile,
	home.StateDir, home.CacheDir, home.BackupDir, home.TmpDir, home.SecretsDir, home.LogsDir,
	"skills", "knowledge",
}

// removeData deletes the local data directory: every name of dataNames, then the directory when
// it is empty. kept/ stays when it holds anything. It returns whether the directory is gone, the
// kept/ directory left in place, and the names it left because it does not know them.
func (a *app) removeData(l *local) (removed bool, kept string, left []string) {
	for _, name := range dataNames {
		_ = os.RemoveAll(l.data.Join(name))
	}
	if len(a.keptDirs(l)) > 0 {
		kept = a.showData(l, home.KeptDir) + "/"
	} else {
		_ = os.RemoveAll(l.data.Join(home.KeptDir))
	}
	entries, err := os.ReadDir(l.data.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return true, kept, nil
	}
	for _, e := range entries {
		if e.Name() != home.KeptDir {
			left = append(left, e.Name())
		}
	}
	if len(entries) == 0 && os.Remove(l.data.Path()) == nil {
		return true, kept, nil
	}
	return false, kept, left
}

// uninstallPlanView is the human output of rgd uninstall --plan.
func (a *app) uninstallPlanView(l *local, p *sync.Plan, f uninstallFlags) view {
	var v view
	n := changeCount(p.Items)
	if n == 0 {
		v.head = append(v.head, a.p.T(msg.UninstallNothing))
	} else {
		v.head = append(v.head, a.p.T(msg.UninstallTotal, i18n.Count(n)))
	}
	for _, g := range a.changeGroups(l, p, p.Items) {
		v.add(g)
	}
	v.add(a.uninstallSkipped(p))
	v.foot = append(v.foot, a.p.T(msg.UninstallUntouched, i18n.V("prefix", brand.Prefix)))
	switch {
	case len(f.tools) > 0:
		v.foot = append(v.foot, a.p.T(msg.UninstallRunHintTool, i18n.V("command", brand.CLI+" uninstall")))
	case f.keepHome:
		v.foot = append(v.foot, a.p.T(msg.UninstallRunHintKeep, i18n.V("command", brand.CLI+" uninstall --keep-home"), i18n.V("home", a.showData(l))))
	default:
		v.foot = append(v.foot, a.p.T(msg.UninstallRunHint, i18n.V("command", brand.CLI+" uninstall"), i18n.V("home", a.showData(l))))
	}
	return v
}

// uninstallSkipped lists the tools left as they are.
func (a *app) uninstallSkipped(p *sync.Plan) group {
	g := group{title: a.p.T(msg.UninstallGroupSkipped), warn: true}
	for _, t := range p.Tools {
		if t.Status != sync.ToolUnreadable {
			continue
		}
		name := t.Name
		if name == "" {
			name = t.ID
		}
		g.lines = append(g.lines, line{mark: "?", style: cliout.Red, path: a.p.T(msg.PlanToolUnreadable, i18n.V("tool", name), i18n.V("detail", t.Reason))})
	}
	return g
}

// uninstallResultView is the human output of rgd uninstall.
func (a *app) uninstallResultView(l *local, p *sync.Plan, res *sync.Result, doc uninstallDoc) view {
	var v view
	var done []sync.Item
	var kept []string
	for _, d := range res.Done {
		done = append(done, d.Item)
		if d.Kept != "" && !slices.Contains(kept, d.Kept) {
			kept = append(kept, d.Kept)
		}
	}
	for _, g := range a.changeGroups(l, p, done) {
		v.add(g)
	}
	names := toolNames(p)
	retry := group{title: a.p.T(msg.UninstallGroupRetry), warn: true}
	for _, r := range res.Retry {
		retry.lines = append(retry.lines, a.itemLine(l, p, names, r.Item))
	}
	v.add(retry)
	v.add(a.uninstallSkipped(p))

	n := changeCount(done)
	if n == 0 && len(res.Retry) == 0 {
		v.head = append(v.head, a.p.T(msg.UninstallNothing))
	} else {
		v.foot = append(v.foot, a.p.T(msg.UninstallDone, i18n.Count(n)))
	}
	h := doc.Home
	switch {
	case h.Removed && h.Kept == "":
		v.foot = append(v.foot, a.p.T(msg.UninstallHomeRemoved, i18n.V("home", h.Path)))
	case h.Kept != "":
		v.foot = append(v.foot, a.out.Paint(cliout.Yellow, a.p.T(msg.UninstallHomeKept, i18n.V("kept", h.Kept))))
	case h.Reason == "keep_home":
		v.foot = append(v.foot, a.p.T(msg.UninstallHomeKeep, i18n.V("home", h.Path)))
	case h.Reason == "tool":
		v.foot = append(v.foot, a.p.T(msg.UninstallHomeTool, i18n.V("home", h.Path)))
	case h.Reason == "entries_left":
		v.foot = append(v.foot, a.out.Paint(cliout.Yellow, a.p.T(msg.UninstallHomeEntriesLeft, i18n.V("home", h.Path))))
	case h.Reason == "not_empty":
		v.foot = append(v.foot, a.p.T(msg.UninstallHomeNotEmpty, i18n.V("home", h.Path), i18n.V("names", strings.Join(h.Left, ", "))))
	}
	if h.Kept == "" {
		for _, k := range kept {
			v.foot = append(v.foot, a.p.T(msg.SyncKept, i18n.V("path", a.showData(l, strings.Split(k, "/")...))))
		}
	}
	return v
}
