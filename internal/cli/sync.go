// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/gitrepo"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/localconfig"
	"github.com/code2rich/rigdepot/internal/sync"
)

func init() { register("", newSyncCmd) }

// syncFlags are the options of rgd sync.
type syncFlags struct {
	src        sourceFlags
	plan       bool
	skipGlobal bool
	yesGlobal  bool
	tools      []string
	overwrite  []string
	exitCode   bool
}

// newSyncCmd: rgd sync. It reads the resource repository, plans what to write into every AI
// tool, previews the plan in a sandbox, asks for consent to change global configuration, and
// writes. With --plan it stops after the preview and changes nothing.
//
// Exit codes: 0 done; 3 something waits for the person (global changes not agreed to, local
// changes not overwritten, a tool whose configuration cannot be read, a target that changed
// while syncing); 5 with --plan --exit-code when there is something to change; 9 the manifest
// or the selection cannot be read, or another rgd holds the lock.
func newSyncCmd(a *app) *cobra.Command {
	var f syncFlags
	cmd := &cobra.Command{
		Use:   "sync",
		Short: a.p.T(msg.SyncShort),
		Args:  a.noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if f.skipGlobal && f.yesGlobal {
				return a.usageError("skip_and_yes_global", a.p.T(msg.SyncSkipAndYes), cmd.CommandPath())
			}
			if f.exitCode && !f.plan {
				return a.usageError("exit_code_without_plan", a.p.T(msg.SyncExitCodeNeedsPlan), cmd.CommandPath())
			}
			return a.runSync(cmd.Context(), cmd, f)
		},
	}
	a.addSourceFlags(cmd, &f.src)
	fs := cmd.Flags()
	fs.BoolVar(&f.plan, "plan", false, a.p.T(msg.FlagSyncPlan))
	fs.BoolVar(&f.skipGlobal, "skip-global", false, a.p.T(msg.FlagSkipGlobal))
	fs.BoolVar(&f.yesGlobal, "yes-global", false, a.p.T(msg.FlagYesGlobal))
	fs.StringArrayVar(&f.tools, "tool", nil, a.p.T(msg.FlagTool))
	fs.StringArrayVar(&f.overwrite, "overwrite", nil, a.p.T(msg.FlagOverwrite))
	fs.BoolVar(&f.exitCode, "exit-code", false, a.p.T(msg.FlagExitCode))
	return cmd
}

func (a *app) runSync(ctx context.Context, cmd *cobra.Command, f syncFlags) error {
	l, err := a.local()
	if err != nil {
		return err
	}
	if f.yesGlobal {
		// --yes-global counts only from the command line (localconfig: FlagOnly).
		if err := l.cfg.SetFlag(localconfig.KeyYesGlobal, "--yes-global", "true"); err != nil {
			return err
		}
	}
	st, rem, err := a.resolveSource(ctx, l, f.src)
	if errors.Is(err, errNoSource) {
		return a.noSourceError(cmd.CommandPath())
	}
	if err != nil {
		return err
	}
	for _, code := range f.overwrite {
		if !st.Known(code) {
			a.out.Warn(a.p.T(msg.SyncOverwriteUnknown, i18n.V("resource", code)))
		}
	}
	opt := sync.Options{
		Tools:     f.tools,
		Disabled:  l.cfg.List(localconfig.KeyToolsDisabled),
		Overwrite: f.overwrite,
		// Without a server, the only way to hand a tool an MCP server's secrets is the tool's
		// own environment variable reference (decision D5).
		// TODO(M1-28, M1-2A): with a server, mcp.mode (and the server's default below it) once
		// the sealed secrets reach this computer and rgd mcp run can hand them over.
		MCPMode: adapter.MCPNativeEnv,
	}
	rem.apply(&opt)
	if f.plan {
		return a.syncPlan(ctx, l, st, opt, f)
	}

	s, err := l.eng.Lock(ctx)
	if err != nil {
		return a.stateError(err)
	}
	defer s.Release()
	p, err := l.eng.Plan(ctx, st, opt)
	if err != nil {
		return a.stateError(err)
	}
	prev, err := l.eng.Preview(ctx, p)
	if err != nil {
		return a.previewError(err)
	}
	in := sync.ConsentInput{
		SkipGlobal:  f.skipGlobal,
		YesGlobal:   l.cfg.Bool(localconfig.KeyYesGlobal),
		Interactive: a.interactive(l.cfg),
	}
	if in.Interactive {
		in.Ask = a.asker(l, p)
	}
	ap := p.Approve(in, time.Now())
	res, applyErr := s.Apply(ctx, p, ap)
	if res == nil {
		return a.stateError(applyErr)
	}
	// What the command line keeps for later (workflows, knowledge bases) follows a run that went
	// through; after a failure the run stopped, and so does this.
	var kept *sync.Local
	if applyErr == nil {
		if kept, applyErr = s.KeepLocal(ctx, st, a.knowledgeFetcher(l)); kept == nil {
			kept = &sync.Local{}
		}
	}
	doc := syncDoc{Plan: a.newPlanDoc(p, st, &prev), Done: []doneDoc{}, Deferred: []opDoc{}, Retry: []retryDoc{}, Manifest: a.showData(l, home.ManifestFile)}
	if kept != nil {
		doc.Plan.Knowledge = a.knowledgeDocs(l, kept)
	}
	for _, d := range res.Done {
		dd := doneDoc{opDoc: newOpDoc(d.Item, false), Kept: d.Kept, Outcome: string(d.Outcome), Leftover: d.Leftover}
		doc.Done = append(doc.Done, dd)
	}
	for _, it := range res.Deferred {
		doc.Deferred = append(doc.Deferred, newOpDoc(it, true))
	}
	for _, r := range res.Retry {
		doc.Retry = append(doc.Retry, retryDoc{opDoc: newOpDoc(r.Item, false), Error: r.Err.Error()})
	}
	var failure *cliout.Error
	if applyErr != nil {
		failure = cliout.Fail(cliout.ExitFailure, "write_failed", a.p.T(msg.SyncWriteFailed, i18n.V("detail", applyErr.Error())))
		failure.Hint = a.p.T(msg.SyncWriteFailedHint)
		failure.Err = applyErr
		doc.Error = &errorField{Code: failure.Code, Message: failure.Message}
	}
	exit := syncExit(p, res, kept)
	if rem != nil && !rem.state.Cached {
		a.report(ctx, rem, st, p, res, failure != nil || exit != nil && cliout.ExitOf(exit) == cliout.ExitFailure, exit != nil)
		a.newerHint(rem)
	}
	if err := a.out.Result(cliout.Schema("sync", 1), doc, func(w io.Writer) error {
		return a.writeView(w, a.syncResultView(l, p, st, res, kept, f))
	}); err != nil {
		return err
	}
	if failure != nil {
		return failure
	}
	return exit
}

// knowledgeFetcher is the person's own git for the knowledge bases. Their addresses come from the
// resource repository, not from the person, so a local path is not accepted.
func (a *app) knowledgeFetcher(l *local) sync.Fetcher {
	if a.env.Knowledge != nil {
		return a.env.Knowledge
	}
	f, err := gitrepo.New(gitrepo.Options{Git: l.cfg.String(localconfig.KeyGit), CacheDir: l.data.Join(home.CacheDir, "repos")})
	if err != nil {
		return nil // every knowledge base reports that there is no git
	}
	return f
}

// knowledgeDocs is what a sync did about the knowledge bases, for --json.
func (a *app) knowledgeDocs(l *local, kept *sync.Local) []knowledgeDoc {
	out := []knowledgeDoc{}
	for _, pl := range kept.Knowledge {
		d := knowledgeDoc{Resource: pl.Resource.Code, Repo: pl.Source.URL, Ref: pl.Source.Ref, Dir: pl.Source.Dir, Path: pl.Path}
		if pl.Err != nil {
			d.Error = pl.Err.Error()
		} else {
			files := pl.Files
			d.Commit, d.Files = pl.Commit, &files
		}
		out = append(out, d)
	}
	return out
}

// knowledgeGroup lists the knowledge bases: in a plan, what a sync fetches; after a sync, what it
// fetched and what it could not.
func (a *app) knowledgeGroup(l *local, st *desired.State, kept *sync.Local) group {
	g := group{title: a.p.T(msg.SyncGroupKnowledge)}
	if kept == nil {
		for _, r := range st.Resources {
			if pl, ok := r.Spec.(kind.Pulled); ok {
				g.lines = append(g.lines, line{mark: "+", style: cliout.Green, path: a.showData(l, home.KnowledgeDir, r.Ref.Code) + "/",
					note: a.p.T(msg.SyncKnowledgeFrom, i18n.V("repo", pl.Pull().URL))})
			}
		}
		return g
	}
	for _, pl := range kept.Knowledge {
		ln := line{mark: "+", style: cliout.Green, path: a.showData(l, home.KnowledgeDir, pl.Resource.Code) + "/"}
		if pl.Err != nil {
			ln.mark, ln.style = "!", cliout.Yellow
			ln.note = a.p.T(msg.SyncKnowledgeFailed, i18n.V("detail", pl.Err.Error()))
			g.warn = true
		} else {
			ln.note = a.p.T(msg.SyncKnowledgeFetched, i18n.Count(pl.Files), i18n.V("commit", shortCommit(pl.Commit)))
		}
		g.lines = append(g.lines, ln)
	}
	for _, code := range kept.KnowledgeRemoved {
		g.lines = append(g.lines, line{mark: "-", style: cliout.Red, path: a.showData(l, home.KnowledgeDir, code) + "/"})
	}
	return g
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// syncExit is the exit code of a sync that ran to the end.
func syncExit(p *sync.Plan, res *sync.Result, kept *sync.Local) error {
	for _, t := range p.Tools {
		if t.Status == sync.ToolFailed {
			return cliout.Status(cliout.ExitFailure)
		}
	}
	pending := res.Attention() || len(p.Attention()) > 0 || (kept != nil && kept.Failed()) ||
		slices.ContainsFunc(p.Tools, func(t sync.Tool) bool { return t.Status == sync.ToolUnreadable })
	if pending {
		return cliout.Status(cliout.ExitPending)
	}
	return nil
}

// syncPlan is rgd sync --plan: plan and preview, without the lock and without changing anything
// the person or a tool owns.
func (a *app) syncPlan(ctx context.Context, l *local, st *desired.State, opt sync.Options, f syncFlags) error {
	p, err := l.eng.Plan(ctx, st, opt)
	if err != nil {
		return a.stateError(err)
	}
	prev, err := l.eng.Preview(ctx, p)
	if err != nil {
		return a.previewError(err)
	}
	if err := a.out.Result(cliout.Schema("plan", 1), a.newPlanDoc(p, st, &prev), func(w io.Writer) error {
		return a.writeView(w, a.planView(l, p, st, f))
	}); err != nil {
		return err
	}
	if f.exitCode && len(p.Changes()) > 0 {
		return cliout.Status(cliout.ExitDiff)
	}
	return nil
}

// previewError: carrying the plan out in the sandbox failed. Nothing was written.
func (a *app) previewError(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	e := cliout.Fail(cliout.ExitFailure, "preview_failed", a.p.T(msg.SyncPreviewFailed, i18n.V("detail", err.Error())))
	e.Err = err
	return e
}

// planView is the human output of rgd sync --plan.
func (a *app) planView(l *local, p *sync.Plan, st *desired.State, f syncFlags) view {
	var v view
	v.head = append(v.head, a.out.Paint(cliout.Dim, a.detectedLine(p)))
	n := changeCount(p.Items)
	if n == 0 {
		v.head = append(v.head, a.p.T(msg.PlanNothing))
	} else {
		v.head = append(v.head, a.p.T(msg.PlanTotal, i18n.Count(n)))
	}
	for _, g := range a.changeGroups(l, p, p.Items) {
		v.add(g)
	}
	v.add(a.knowledgeGroup(l, st, nil))
	v.add(a.attentionGroup(l, p))
	v.add(a.skippedGroup(p, st))
	v.foot = append(v.foot, a.p.T(msg.PlanUntouched))
	if n > 0 {
		run := commandLine("sync", f.src)
		if hasGlobal(p.Items) {
			v.foot = append(v.foot, a.p.T(msg.PlanRunHintGlobal, i18n.V("command", run), i18n.V("skip", run+" --skip-global")))
		} else {
			v.foot = append(v.foot, a.p.T(msg.PlanRunHint, i18n.V("command", run)))
		}
	}
	return v
}

// syncResultView is the human output of rgd sync: what was done, what waits, and why.
func (a *app) syncResultView(l *local, p *sync.Plan, st *desired.State, res *sync.Result, local *sync.Local, f syncFlags) view {
	var v view
	v.head = append(v.head, a.out.Paint(cliout.Dim, a.detectedLine(p)))
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
	// The global changes that wait: the same lines, under a heading that says why.
	names := toolNames(p)
	deferred := group{title: a.p.T(msg.SyncGroupDeferred), warn: true}
	for _, it := range res.Deferred {
		deferred.lines = append(deferred.lines, a.itemLine(l, p, names, it))
	}
	v.add(deferred)
	retry := group{title: a.p.T(msg.SyncGroupRetry), warn: true}
	for _, r := range res.Retry {
		retry.lines = append(retry.lines, a.itemLine(l, p, names, r.Item))
	}
	v.add(retry)
	if local != nil {
		v.add(a.knowledgeGroup(l, st, local))
	}
	v.add(a.attentionGroup(l, p))
	v.add(a.skippedGroup(p, st))

	n := changeCount(done)
	if n == 0 && len(res.Deferred) == 0 && len(res.Retry) == 0 {
		v.foot = append(v.foot, a.p.T(msg.SyncNothing))
	} else {
		v.foot = append(v.foot, a.p.T(msg.SyncDone, i18n.Count(n), i18n.V("manifest", a.showData(l, home.ManifestFile))))
	}
	for _, k := range kept {
		v.foot = append(v.foot, a.p.T(msg.SyncKept, i18n.V("path", a.showData(l, strings.Split(k, "/")...))))
	}
	if len(res.Deferred) > 0 {
		run := commandLine("sync", f.src)
		if f.skipGlobal {
			v.foot = append(v.foot, a.p.T(msg.SyncDeferredSkipped, i18n.V("command", run)))
		} else {
			v.foot = append(v.foot, a.p.T(msg.SyncDeferredHint, i18n.V("command", run+" --yes-global")))
		}
	}
	if len(res.Retry) > 0 {
		v.foot = append(v.foot, a.p.T(msg.SyncRetryHint, i18n.V("command", commandLine("sync", f.src))))
	}
	v.foot = append(v.foot, a.p.T(msg.SyncUndoHint, i18n.V("command", brand.CLI+" uninstall")))
	return v
}

// syncDoc is the JSON output of rgd sync (schema rgd.sync/v1): the plan, and what was done.
type syncDoc struct {
	Plan     planDoc     `json:"plan"`
	Done     []doneDoc   `json:"done"`
	Deferred []opDoc     `json:"deferred"`
	Retry    []retryDoc  `json:"retry"`
	Manifest string      `json:"manifest"`
	Error    *errorField `json:"error,omitempty"`
}

type doneDoc struct {
	opDoc
	// Kept is where a changed target was copied first, relative to the local data directory.
	Kept     string `json:"kept,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	Leftover string `json:"leftover,omitempty"`
}

type retryDoc struct {
	opDoc
	Error string `json:"error"`
}

// errorField is a failure that stopped the run after part of it was done.
type errorField struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// asker asks the person, file by file, whether a tool's global configuration may be changed.
// The question goes to standard error; the answer is read from standard input.
func (a *app) asker(l *local, p *sync.Plan) func(sync.Consent) bool {
	in := bufio.NewReader(a.env.Stdin)
	yes := strings.Split(a.p.T(msg.SyncAskYes), ",")
	names := toolNames(p)
	return func(c sync.Consent) bool {
		var keys []string
		for _, it := range p.Items {
			if it.Tool == c.Tool && it.Target.Path == c.File && it.Global && it.Action.Changes() {
				keys = append(keys, strings.Join(it.Target.Key, "."))
			}
		}
		fmt.Fprint(a.out.Stderr(), "  "+a.out.Paint(cliout.Yellow, "?")+" "+a.p.T(msg.SyncAsk,
			i18n.V("file", a.showTarget(l.userHome, c.File)), i18n.V("tool", names[c.Tool]),
			i18n.V("keys", strings.Join(keys, ", ")), i18n.V("backup", a.showData(l, home.BackupDir)+"/"))+" ")
		answer, _ := in.ReadString('\n')
		answer = strings.ToLower(strings.TrimSpace(answer))
		return answer != "" && slices.Contains(yes, answer)
	}
}
