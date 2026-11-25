// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/executor"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/manifest"
	"github.com/code2rich/rigdepot/internal/sandbox"
	"github.com/code2rich/rigdepot/internal/sync"
)

// This file turns a plan of the sync engine into the output of sync and uninstall: the JSON
// document (rgd.plan/v1, also part of rgd.sync/v1 and rgd.uninstall/v1) and the human text,
// laid out like the terminal demo of the prototype's setup guide: the tools found, one line
// with the total, then one group per kind of change, one line per target.

// planDoc is the JSON form of a plan.
type planDoc struct {
	Uninstall bool          `json:"uninstall"`
	Source    *sourceDoc    `json:"source,omitempty"`
	Tools     []toolDoc     `json:"tools"`
	Ops       []opDoc       `json:"ops"`
	Attention []opDoc       `json:"attention"`
	Degraded  []degradedDoc `json:"degraded"`
	Shared    []sharedDoc   `json:"shared"`
	Skipped   []skipDoc     `json:"skipped"`
	Failed    []failedDoc   `json:"failed"`
	Preview   []changeDoc   `json:"preview"`
	Summary   summaryDoc    `json:"summary"`
	// Knowledge are the knowledge bases a sync fetches with the person's own git.
	Knowledge []knowledgeDoc `json:"knowledge"`
}

// knowledgeDoc is a knowledge base fetched into the local data directory, or to be.
type knowledgeDoc struct {
	Resource string `json:"resource"`
	Repo     string `json:"repo"`
	Ref      string `json:"ref,omitempty"`
	Dir      string `json:"dir,omitempty"`
	// Path is where its content is on this computer.
	Path string `json:"path"`
	// Commit, Files and Error are what a sync did; absent in a plan.
	Commit string `json:"commit,omitempty"`
	Files  *int   `json:"files,omitempty"`
	Error  string `json:"error,omitempty"`
}

type sourceDoc struct {
	Type   string `json:"type"`
	URL    string `json:"url"`
	Ref    string `json:"ref,omitempty"`
	Dir    string `json:"dir,omitempty"`
	Commit string `json:"commit,omitempty"`
}

type toolDoc struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Held are the resources whose earlier writes stay as they are this run.
	Held []string `json:"held,omitempty"`
}

// opDoc is one target. kind is the manifest operation (tree, file, key); key is the key path,
// one element per level.
type opDoc struct {
	Action       string   `json:"action"`
	Tool         string   `json:"tool"`
	Resource     string   `json:"resource"`
	ResourceKind string   `json:"resourceKind,omitempty"`
	Version      int      `json:"version,omitempty"`
	Kind         string   `json:"kind"`
	Path         string   `json:"path"`
	Key          []string `json:"key,omitempty"`
	Files        int      `json:"files,omitempty"`
	NeedsConsent bool     `json:"needsConsent"`
	SharedWith   []string `json:"sharedWith,omitempty"`
	Degraded     string   `json:"degraded,omitempty"`
}

type degradedDoc struct {
	Tool     string `json:"tool"`
	Resource string `json:"resource"`
	Note     string `json:"note"`
	Message  string `json:"message"`
}

type sharedDoc struct {
	Tool     string `json:"tool"`
	Resource string `json:"resource"`
	Via      string `json:"via"`
	Path     string `json:"path"`
}

type skipDoc struct {
	Tool     string `json:"tool"`
	Resource string `json:"resource"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail,omitempty"`
}

type failedDoc struct {
	Resource string   `json:"resource"`
	Path     string   `json:"path"`
	Issues   []string `json:"issues"`
}

type changeDoc struct {
	Action string `json:"action"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
}

type summaryDoc struct {
	Create       int `json:"create"`
	Update       int `json:"update"`
	Revert       int `json:"revert"`
	Record       int `json:"record"`
	Attention    int `json:"attention"`
	NeedsConsent int `json:"needsConsent"`
}

// granted returns the files the person agreed to changing in an earlier run.
func granted(p *sync.Plan) map[string]bool {
	out := map[string]bool{}
	for _, c := range p.Consents {
		if c.Granted {
			out[c.Tool+"\x00"+c.File] = true
		}
	}
	return out
}

// needsConsent reports whether carrying out the item still needs the person's consent.
func needsConsent(p *sync.Plan, ok map[string]bool, it sync.Item) bool {
	return it.Global && it.Action.Changes() && !p.Uninstall && !ok[it.Tool+"\x00"+it.Target.Path]
}

func newOpDoc(it sync.Item, consent bool) opDoc {
	d := opDoc{
		Action: string(it.Action), Tool: it.Tool, Resource: it.Resource.Code, ResourceKind: it.Resource.Kind,
		Version: it.Resource.Version, Kind: string(it.Target.Op), Path: it.Target.Path, Key: it.Target.Key,
		NeedsConsent: consent, SharedWith: it.SharedWith, Degraded: it.Degraded,
	}
	if tree, ok := it.Op.(adapter.WriteTree); ok {
		d.Files = len(tree.Files)
	}
	return d
}

func newSourceDoc(s desired.Source) *sourceDoc {
	return &sourceDoc{Type: s.Type, URL: s.URL, Ref: s.Ref, Dir: s.Dir, Commit: s.Commit}
}

// newPlanDoc builds the JSON form of a plan. st is nil for an uninstall.
func (a *app) newPlanDoc(p *sync.Plan, st *desired.State, prev *sandbox.Result) planDoc {
	ok := granted(p)
	d := planDoc{
		Uninstall: p.Uninstall, Tools: []toolDoc{}, Ops: []opDoc{}, Attention: []opDoc{}, Degraded: []degradedDoc{},
		Shared: []sharedDoc{}, Skipped: []skipDoc{}, Failed: []failedDoc{}, Preview: []changeDoc{}, Knowledge: []knowledgeDoc{},
	}
	if st != nil {
		d.Source = newSourceDoc(st.Source)
		for _, r := range st.Resources {
			if pl, ok := r.Spec.(kind.Pulled); ok {
				src := pl.Pull()
				d.Knowledge = append(d.Knowledge, knowledgeDoc{Resource: r.Ref.Code, Repo: src.URL, Ref: src.Ref, Dir: src.Dir,
					Path: home.KnowledgeDir + "/" + r.Ref.Code})
			}
		}
		for _, f := range st.Failed {
			fd := failedDoc{Resource: f.Ref.Code, Path: f.Path, Issues: []string{}}
			for _, is := range f.Issues {
				fd.Issues = append(fd.Issues, is.Code)
			}
			d.Failed = append(d.Failed, fd)
		}
	}
	for _, t := range p.Tools {
		d.Tools = append(d.Tools, toolDoc{ID: t.ID, Name: t.Name, Status: string(t.Status), Version: t.Detection.Version, Reason: t.Reason, Held: t.Held})
		for _, dg := range t.Degraded {
			d.Degraded = append(d.Degraded, degradedDoc{Tool: t.ID, Resource: dg.Resource.Code, Note: dg.Note, Message: a.p.T(dg.Note)})
		}
		for _, sh := range t.Shared {
			d.Shared = append(d.Shared, sharedDoc{Tool: t.ID, Resource: sh.Resource.Code, Via: sh.Via, Path: sh.Path})
		}
		for _, sk := range t.Skips {
			d.Skipped = append(d.Skipped, skipDoc{Tool: t.ID, Resource: sk.Resource.Code, Reason: string(sk.Reason), Detail: sk.Detail})
		}
	}
	for _, it := range p.Items {
		switch {
		case it.Action.Changes():
			c := needsConsent(p, ok, it)
			d.Ops = append(d.Ops, newOpDoc(it, c))
			if c {
				d.Summary.NeedsConsent++
			}
		case it.Action.Attention():
			d.Attention = append(d.Attention, newOpDoc(it, false))
		}
	}
	s := p.Summary()
	d.Summary.Create, d.Summary.Update, d.Summary.Revert, d.Summary.Record, d.Summary.Attention = s.Create, s.Update, s.Revert, s.Record, s.Attention
	if prev != nil {
		for _, c := range prev.Changes {
			d.Preview = append(d.Preview, changeDoc{Action: string(c.Action), Kind: string(c.Kind), Path: c.Path})
		}
	}
	return d
}

// line is one target in the human output: a mark, a path and what happens there.
type line struct {
	mark  string
	style cliout.Style
	path  string
	note  string
}

// group is a heading and its lines.
type group struct {
	title string
	warn  bool
	lines []line
}

// view is what the human output shows: the groups, in order, and the lines around them.
type view struct {
	head   []string // before the groups: the tools found, the total
	groups []group
	foot   []string // after the groups: what is not changed, what to run next
}

func (v *view) add(g group) {
	if len(g.lines) > 0 {
		v.groups = append(v.groups, g)
	}
}

// write writes the view, the paths of every group aligned in one column.
func (a *app) writeView(w io.Writer, v view) error {
	width := 0
	for _, g := range v.groups {
		for _, l := range g.lines {
			width = max(width, cliout.DisplayWidth(l.path))
		}
	}
	width = min(width, 52)
	var b strings.Builder
	for _, h := range v.head {
		b.WriteString("  " + h + "\n")
	}
	for _, g := range v.groups {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		style := cliout.Bold
		if g.warn {
			style = cliout.Yellow
		}
		b.WriteString("  " + a.out.Paint(style, g.title) + "\n")
		for _, l := range g.lines {
			text := l.path
			if l.note != "" {
				text = cliout.PadRight(l.path, width) + "  " + a.out.Paint(cliout.Dim, l.note)
			}
			b.WriteString("  " + a.out.Paint(l.style, l.mark) + " " + text + "\n")
		}
	}
	if len(v.foot) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		for _, f := range v.foot {
			b.WriteString("  " + f + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// toolNames maps tool identifiers to their names.
func toolNames(p *sync.Plan) map[string]string {
	out := map[string]string{}
	for _, t := range p.Tools {
		out[t.ID] = t.ID
		if t.Name != "" {
			out[t.ID] = t.Name
		}
	}
	return out
}

func namesOf(ids []string, names map[string]string) string {
	var out []string
	for _, id := range ids {
		if n := names[id]; n != "" {
			out = append(out, n)
		} else {
			out = append(out, id)
		}
	}
	return strings.Join(out, ", ")
}

// detectedLine is the first line: the tools found on this computer.
func (a *app) detectedLine(p *sync.Plan) string {
	var found []string
	for _, t := range p.Tools {
		if t.Detection.Status != adapter.Installed {
			continue
		}
		name := t.Name
		if name == "" {
			name = t.ID
		}
		if t.Detection.Version != "" {
			name += " " + t.Detection.Version
		}
		found = append(found, name)
	}
	if len(found) == 0 {
		return a.p.T(msg.PlanNoTools)
	}
	return a.p.T(msg.PlanDetected, i18n.V("tools", strings.Join(found, " · ")))
}

// targetPath shows an item's target; a directory ends with a slash.
func (a *app) targetPath(l *local, it sync.Item) string {
	p := a.showTarget(l.userHome, it.Target.Path)
	if it.Target.Op == manifest.OpTree {
		sep := "/"
		if a.env.GOOS == "windows" {
			sep = `\`
		}
		p += sep
	}
	return p
}

// itemLine is the line for an item that changes something.
func (a *app) itemLine(l *local, p *sync.Plan, names map[string]string, it sync.Item) line {
	ln := line{path: a.targetPath(l, it)}
	var notes []string
	files := 0
	if tree, ok := it.Op.(adapter.WriteTree); ok {
		files = len(tree.Files)
	}
	key := strings.Join(it.Target.Key, ".")
	switch it.Action {
	case sync.ActionCreate, sync.ActionRestore:
		ln.mark, ln.style = "+", cliout.Green
		switch it.Target.Op {
		case manifest.OpTree:
			notes = append(notes, a.p.T(msg.PlanNoteNewDir), a.p.T(msg.PlanNoteFiles, i18n.Count(files)))
		case manifest.OpFile:
			notes = append(notes, a.p.T(msg.PlanNoteNewFile))
		case manifest.OpKey:
			notes = append(notes, a.p.T(msg.PlanNoteKeyAdd, i18n.V("key", key)))
		}
		if it.Action == sync.ActionRestore {
			notes = append(notes, a.p.T(msg.PlanNoteRestore))
		}
	case sync.ActionUpdate, sync.ActionOverwrite:
		ln.mark, ln.style = "~", cliout.Cyan
		switch it.Target.Op {
		case manifest.OpTree:
			notes = append(notes, a.p.T(msg.PlanNoteUpdateDir), a.p.T(msg.PlanNoteFiles, i18n.Count(files)))
		case manifest.OpFile:
			notes = append(notes, a.p.T(msg.PlanNoteUpdateFile))
		case manifest.OpKey:
			notes = append(notes, a.p.T(msg.PlanNoteKeyChange, i18n.V("key", key)))
		}
		if it.Action == sync.ActionOverwrite {
			notes = append(notes, a.p.T(msg.PlanNoteOverwrite, i18n.V("kept", a.showData(l, home.KeptDir)+"/")))
		}
	case sync.ActionRevert:
		ln.mark, ln.style = "-", cliout.Red
		if it.Target.Op == manifest.OpKey {
			notes = append(notes, a.p.T(msg.PlanNoteKeyRemove, i18n.V("key", key)))
		} else {
			notes = append(notes, a.p.T(msg.PlanNoteRemove, i18n.V("resource", it.Resource.Code), i18n.V("tool", names[it.Tool])))
		}
		if it.Entry != nil && it.Observed.State == executor.Present && it.Observed.Digest != it.Entry.Digest {
			notes = append(notes, a.p.T(msg.PlanNoteRemoveKept, i18n.V("kept", a.showData(l, home.KeptDir)+"/")))
		}
		if len(it.SharedWith) > 0 {
			notes = append(notes, a.p.T(msg.PlanNoteSharedLoses, i18n.V("tools", namesOf(it.SharedWith, names))))
		}
	}
	if it.Target.Op == manifest.OpKey {
		// The file is changed either way: the mark of the setup guide for global configuration.
		ln.mark, ln.style = "~", cliout.Yellow
	}
	if it.Action.Writes() && len(it.SharedWith) > 0 {
		notes = append(notes, a.p.T(msg.PlanNoteSharedReads, i18n.V("tools", namesOf(it.SharedWith, names))))
	}
	if it.Degraded != "" {
		notes = append(notes, a.p.T(it.Degraded))
	}
	ln.note = strings.Join(notes, " · ")
	return ln
}

// attentionLine is the line for something left for the person.
func (a *app) attentionLine(l *local, it sync.Item) line {
	ln := line{mark: "!", style: cliout.Yellow, path: a.targetPath(l, it)}
	cmd := brand.CLI + " sync --overwrite " + it.Resource.Code
	switch it.Action {
	case sync.ActionTaken:
		ln.note = a.p.T(msg.PlanNoteTaken)
	case sync.ActionModified:
		ln.note = a.p.T(msg.PlanNoteModified, i18n.V("command", cmd))
	case sync.ActionDeleted:
		ln.note = a.p.T(msg.PlanNoteDeleted, i18n.V("command", cmd))
	}
	return ln
}

// changeGroups sorts items that change something into the groups of the human output. ok are
// the files agreed to before; consent says whether global changes still need it at all.
func (a *app) changeGroups(l *local, p *sync.Plan, items []sync.Item) []group {
	names := toolNames(p)
	ok := granted(p)
	var created, updated, removed, global, globalAgreed group
	created.title = a.p.T(msg.PlanGroupNew, i18n.V("prefix", brand.Prefix))
	updated.title = a.p.T(msg.PlanGroupUpdated)
	removed.title = a.p.T(msg.PlanGroupRemoved)
	global.title, global.warn = a.p.T(msg.PlanGroupGlobal, i18n.V("backup", a.showData(l, home.BackupDir)+"/")), true
	globalAgreed.title = a.p.T(msg.PlanGroupGlobalAgreed, i18n.V("backup", a.showData(l, home.BackupDir)+"/"))
	for _, it := range items {
		if !it.Action.Changes() {
			continue
		}
		ln := a.itemLine(l, p, names, it)
		switch {
		case it.Global && needsConsent(p, ok, it):
			global.lines = append(global.lines, ln)
		case it.Global:
			globalAgreed.lines = append(globalAgreed.lines, ln)
		case it.Action == sync.ActionRevert:
			removed.lines = append(removed.lines, ln)
		case it.Action == sync.ActionCreate || it.Action == sync.ActionRestore:
			created.lines = append(created.lines, ln)
		default:
			updated.lines = append(updated.lines, ln)
		}
	}
	if p.Uninstall {
		removed.title = a.p.T(msg.UninstallGroupRemoved)
		globalAgreed.title = a.p.T(msg.UninstallGroupGlobal, i18n.V("backup", a.showData(l, home.BackupDir)+"/"))
	}
	return []group{created, updated, removed, global, globalAgreed}
}

// attentionGroup lists what is left for the person.
func (a *app) attentionGroup(l *local, p *sync.Plan) group {
	g := group{title: a.p.T(msg.PlanGroupAttention), warn: true}
	for _, it := range p.Attention() {
		g.lines = append(g.lines, a.attentionLine(l, it))
	}
	return g
}

// skipReason is the text for why a resource did not reach a tool.
func (a *app) skipReason(sk adapter.Skip) string {
	key := msg.PlanSkipOther
	switch sk.Reason {
	case adapter.Unsupported:
		key = msg.PlanSkipUnsupported
	case adapter.NeedsSecretSupport:
		key = msg.PlanSkipNeedsSecret
	case adapter.NameTaken:
		key = msg.PlanSkipNameTaken
	case adapter.ConfigUnwritable:
		key = msg.PlanSkipUnwritable
	case adapter.PathTooLong:
		key = msg.PlanSkipPathTooLong
	}
	return a.p.T(key, i18n.V("reason", string(sk.Reason)))
}

// skippedGroup lists the tools that did not take part, the resources a tool could not take and
// the resources the repository could not give.
func (a *app) skippedGroup(p *sync.Plan, st *desired.State) group {
	g := group{title: a.p.T(msg.PlanGroupSkipped)}
	var notInstalled []string
	for _, t := range p.Tools {
		name := t.Name
		if name == "" {
			name = t.ID
		}
		tool := i18n.V("tool", name)
		reason := i18n.V("detail", t.Reason)
		var note string
		switch t.Status {
		case sync.ToolNotInstalled:
			notInstalled = append(notInstalled, name)
		case sync.ToolUnknown:
			note = a.p.T(msg.PlanToolUnknown, tool, i18n.V("detail", a.p.T(t.Reason)))
		case sync.ToolUnreadable:
			note = a.p.T(msg.PlanToolUnreadable, tool, reason)
		case sync.ToolFailed:
			note = a.p.T(msg.PlanToolFailed, tool, reason)
		case sync.ToolNotSelected:
			note = a.p.T(msg.PlanToolNotSelected, tool)
		case sync.ToolDisabled:
			note = a.p.T(msg.PlanToolDisabled, tool, i18n.V("key", "tools.disabled"))
		}
		if note != "" {
			g.lines = append(g.lines, line{mark: "x", style: cliout.Yellow, path: note})
		}
		for _, sk := range t.Skips {
			path := name + " · " + sk.Resource.Code
			note := a.skipReason(sk)
			if sk.Detail != "" {
				note += " (" + sk.Detail + ")"
			}
			g.lines = append(g.lines, line{mark: "x", style: cliout.Dim, path: path, note: note})
		}
	}
	if len(notInstalled) > 0 {
		g.lines = append(g.lines, line{mark: "x", style: cliout.Dim, path: a.p.T(msg.PlanToolsNotInstalled, i18n.V("tools", strings.Join(notInstalled, ", ")))})
	}
	if st != nil {
		for _, f := range st.Failed {
			var codes []string
			for _, is := range f.Issues {
				if !slices.Contains(codes, is.Code) {
					codes = append(codes, is.Code)
				}
			}
			g.lines = append(g.lines, line{mark: "x", style: cliout.Yellow, path: f.Path,
				note: a.p.T(msg.PlanResourceFailed, i18n.V("issues", strings.Join(codes, ", ")))})
		}
	}
	return g
}

// changeCount is the number of targets a list of items changes.
func changeCount(items []sync.Item) int {
	n := 0
	for _, it := range items {
		if it.Action.Changes() {
			n++
		}
	}
	return n
}

// hasGlobal reports whether any item changes a tool's global configuration.
func hasGlobal(items []sync.Item) bool {
	return slices.ContainsFunc(items, func(it sync.Item) bool { return it.Global && it.Action.Changes() })
}

// commandLine is how the person ran the command, for the hints: the command and --repo when
// they gave one.
func commandLine(name string, f sourceFlags) string {
	s := brand.CLI + " " + name
	if f.repo != "" {
		s += " --repo " + quoteArg(f.repo)
	}
	if f.branch != "" {
		s += " --branch " + quoteArg(f.branch)
	}
	if f.dir != "" {
		s += " --dir " + quoteArg(f.dir)
	}
	for _, p := range f.products {
		s += " --product " + quoteArg(p)
	}
	return s
}

func quoteArg(s string) string {
	if strings.ContainsAny(s, " \t'\"") {
		return fmt.Sprintf("%q", s)
	}
	return s
}
