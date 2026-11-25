// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/executor"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// Action is what a plan does at one target. The values are stable names for machine-readable
// output.
type Action string

const (
	// ActionCreate: nothing is there and the manifest has no entry; write it. Shown "+".
	ActionCreate Action = "create"
	// ActionUpdate: what is there is what we wrote, and the plan wants something else. "~".
	ActionUpdate Action = "update"
	// ActionOverwrite: the person changed what we wrote and asked to overwrite it
	// (--overwrite); their version is kept in kept/ first. "~".
	ActionOverwrite Action = "overwrite"
	// ActionRestore: the person deleted what we wrote and asked to overwrite (--overwrite). "+".
	ActionRestore Action = "restore"
	// ActionRevert: what we wrote is no longer wanted; undo it. "-".
	ActionRevert Action = "revert"
	// ActionRecord: what is there was changed, and happens to be exactly what the plan wants;
	// only the manifest is updated. Not shown.
	ActionRecord Action = "record"
	// ActionNone: everything is as wanted. Not shown.
	ActionNone Action = "none"
	// ActionTaken: something the manifest does not list is at the target: it belongs to the
	// person and is left alone. "!".
	ActionTaken Action = "taken"
	// ActionModified: the person changed what we wrote; it is not overwritten. "!".
	ActionModified Action = "modified"
	// ActionDeleted: the person deleted what we wrote; it is not put back. "!".
	ActionDeleted Action = "deleted"
)

// Writes reports whether the action writes the target.
func (a Action) Writes() bool {
	return a == ActionCreate || a == ActionUpdate || a == ActionOverwrite || a == ActionRestore
}

// Changes reports whether the action changes something on disk.
func (a Action) Changes() bool { return a.Writes() || a == ActionRevert }

// Attention reports whether the action is something left for the person to look at.
func (a Action) Attention() bool {
	return a == ActionTaken || a == ActionModified || a == ActionDeleted
}

// Item is one target of a plan.
type Item struct {
	// Tool is the adapter's identifier.
	Tool string
	// Resource is the resource the target belongs to. For a revert of a resource the source no
	// longer holds, Kind is empty.
	Resource resource.Ref
	Action   Action
	// Target is what is written or undone; ID is its manifest entry identifier.
	Target executor.Target
	ID     string
	// Op is the operation, for actions that write.
	Op adapter.Op
	// Entry is the manifest entry the target has, if any.
	Entry *manifest.Entry
	// Observed is what the target looked like when the plan was made. Applying checks it is
	// still so.
	Observed executor.Observed
	// Want is the digest the target has after Op.
	Want string
	// Global says the action changes a tool's global configuration (a key, or the tool's own
	// command) and needs the person's consent.
	Global bool
	// Degraded is the note of a resource delivered in a fallback form.
	Degraded string
	// SharedWith lists the other tools that read this target as well (alsoReads). For a revert
	// they lose the resource until their next sync.
	SharedWith []string
}

// ToolStatus says whether a tool takes part in a run.
type ToolStatus string

const (
	// ToolPlanned: the tool takes part.
	ToolPlanned ToolStatus = "planned"
	// ToolNotInstalled: the tool is not on this computer. What was written for it stays.
	ToolNotInstalled ToolStatus = "not_installed"
	// ToolUnknown: detection could not tell; nothing is written or undone.
	ToolUnknown ToolStatus = "unknown"
	// ToolUnreadable: a configuration file or a target could not be read; nothing is written or
	// undone. Reason says what.
	ToolUnreadable ToolStatus = "unreadable"
	// ToolFailed: the adapter could not plan, or planned something the core refuses.
	ToolFailed ToolStatus = "failed"
	// ToolNotSelected: other tools were asked for (--tool).
	ToolNotSelected ToolStatus = "not_selected"
	// ToolDisabled: the configuration says not to write to this tool.
	ToolDisabled ToolStatus = "disabled"
)

// Tool is one tool in a plan.
type Tool struct {
	ID        string
	Name      string
	Detection adapter.Detection
	Status    ToolStatus
	// Reason says why the tool does not take part, for people and logs.
	Reason string
	// Skips are the resources that were not delivered to the tool, and why.
	Skips []adapter.Skip
	// Degraded are the resources delivered in a fallback form.
	Degraded []Degraded
	// Shared are the resources this tool reads from another tool's directory instead of having
	// its own copy.
	Shared []Shared
	// Held lists the resources whose earlier writes were left as they are because their state
	// this run is unknown (could not be read, rendered or planned), by code.
	Held []string
}

// Shared is a resource a tool reads from another tool's directory.
type Shared struct {
	Resource resource.Ref
	// Via is the tool whose copy is read, and Path that copy.
	Via  string
	Path string
}

// Options change what a plan covers.
type Options struct {
	// Tools limits the run to these tools (--tool). Empty means every tool.
	Tools []string
	// Disabled are tools the configuration says not to write to.
	Disabled []string
	// Overwrite are resource codes whose local changes may be overwritten (--overwrite); the
	// changed version is kept in kept/.
	Overwrite []string
	// MCPMode is how MCP servers are written; empty means adapter.MCPNativeEnv.
	MCPMode adapter.MCPMode
	// Launcher is the command that starts the command-line program, as written into the tools'
	// configuration in MCPLauncher and MCPSingleEntry: an absolute path or the program's name.
	// Empty means the name.
	Launcher string
}

// Plan is what a run will do.
type Plan struct {
	// Tools lists every adapter, in identifier order.
	Tools []Tool
	// Items are every target, in the order they are applied: undos first, newest entry first,
	// then writes, tool by tool in the order the adapters planned them.
	Items []Item
	// Source is recorded in the manifest when the plan is applied; nil for an uninstall.
	Source *manifest.Source
	// Uninstall is set for a plan made by PlanUninstall.
	Uninstall bool
	// Consents lists the global configuration files the plan changes, with whether the person
	// agreed to changing each before.
	Consents []Consent

	fingerprint string
	planned     []string            // tools that take part
	shared      map[string][]string // entry id → tools that read it too
	extra       []string            // executor roots outside the home directory
	roots       map[string]map[string]string
	touches     []string // further paths the preview copies: what adapters read
	credentials []string
}

// Changes returns the items that change something on disk.
func (p *Plan) Changes() []Item {
	return slices.DeleteFunc(slices.Clone(p.Items), func(it Item) bool { return !it.Action.Changes() })
}

// Attention returns the items left for the person to look at: names taken, local changes and
// deletions that were not overwritten.
func (p *Plan) Attention() []Item {
	return slices.DeleteFunc(slices.Clone(p.Items), func(it Item) bool { return !it.Action.Attention() })
}

// Summary counts what a plan does, for output.
type Summary struct {
	Create, Update, Revert, Record, Attention, NeedsConsent int
}

// Summary counts the items.
func (p *Plan) Summary() Summary {
	var s Summary
	for _, it := range p.Items {
		switch {
		case it.Action == ActionCreate || it.Action == ActionRestore:
			s.Create++
		case it.Action == ActionUpdate || it.Action == ActionOverwrite:
			s.Update++
		case it.Action == ActionRevert:
			s.Revert++
		case it.Action == ActionRecord:
			s.Record++
		case it.Action.Attention():
			s.Attention++
		}
		if it.Global && it.Action.Changes() && !p.Uninstall {
			s.NeedsConsent++
		}
	}
	return s
}

// fingerprint identifies the content of a manifest, so that Apply can tell that the manifest
// changed since the plan was made.
func fingerprint(m *manifest.Manifest) string {
	data, _ := json.Marshal(m)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// selected says whether a tool takes part, and if not, why.
func selected(t *tool, opt Options) (ToolStatus, string) {
	switch {
	case len(opt.Tools) > 0 && !slices.Contains(opt.Tools, t.id()):
		return ToolNotSelected, ""
	case slices.Contains(opt.Disabled, t.id()):
		return ToolDisabled, ""
	case t.det.Status == adapter.NotInstalled:
		return ToolNotInstalled, ""
	case t.det.Status != adapter.Installed:
		return ToolUnknown, t.det.Reason
	}
	return ToolPlanned, ""
}

// toolPlan is one tool while the plan is computed.
type toolPlan struct {
	t    *tool
	info Tool
	ops  []adapter.Op
	held map[string]bool
	// shared is the result of dedupe, for every tool: entry id → tools that read it too.
	shared map[string][]string
}

// Plan computes what a sync to st does. It reads the manifest without taking the lock, so that
// --plan never waits for another program; Session.Apply makes sure the manifest is still the one
// the plan saw.
//
// A manifest that cannot be read is an error satisfying errors.Is(err, manifest.ErrUnknown).
func (e *Engine) Plan(ctx context.Context, st *desired.State, opt Options) (*Plan, error) {
	if st == nil {
		return nil, errors.New("sync: no desired state")
	}
	if err := e.checkTools(opt.Tools); err != nil {
		return nil, err
	}
	if opt.MCPMode == "" {
		opt.MCPMode = adapter.MCPNativeEnv
	}
	m, err := e.store.Read()
	if err != nil {
		return nil, err
	}
	tools, err := e.detect(ctx)
	if err != nil {
		return nil, err
	}
	p := e.newPlan(m, tools)
	p.Source = &manifest.Source{Type: st.Source.Type, URL: st.Source.URL}

	failed := map[string]bool{}
	for _, f := range st.Failed {
		failed[f.Ref.Code] = true
	}
	var tps []*toolPlan
	for _, t := range tools {
		tp := &toolPlan{t: t, info: Tool{ID: t.id(), Name: t.profile.Name, Detection: t.det}, held: map[string]bool{}}
		tps = append(tps, tp)
		tp.info.Status, tp.info.Reason = selected(t, opt)
		if tp.info.Status != ToolPlanned {
			continue
		}
		for code := range failed {
			tp.held[code] = true
		}
		caps := t.a.Capabilities(t.det)
		r := render(st.Resources, caps, e.renderOptions())
		r.artifacts = launch(r.artifacts, caps, opt)
		tp.info.Degraded = r.degraded
		tp.info.Skips = append(tp.info.Skips, r.skips...)
		for code, err := range r.failed {
			tp.held[code] = true
			tp.info.Skips = append(tp.info.Skips, adapter.Skip{Resource: resource.Ref{Code: code}, Reason: adapter.Unsupported, Detail: "rendering failed: " + err.Error()})
		}
		plan, err := t.a.Plan(ctx, adapter.PlanRequest{Env: t.env, Detection: t.det, Artifacts: r.artifacts, Options: adapter.Options{MCPMode: opt.MCPMode}})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			tp.info.Status, tp.info.Reason = ToolFailed, err.Error()
			continue
		}
		if i := slices.IndexFunc(plan.Skips, func(s adapter.Skip) bool { return s.Reason == adapter.ConfigUnreadable }); i >= 0 {
			// Not being able to read the configuration is not knowing what is in it: nothing at
			// all is written to this tool in this run.
			tp.info.Status, tp.info.Reason = ToolUnreadable, plan.Skips[i].Detail
			continue
		}
		tp.info.Skips = append(tp.info.Skips, plan.Skips...)
		for _, op := range plan.Ops {
			if x, ok := op.(adapter.Exec); ok {
				tp.info.Skips = append(tp.info.Skips, adapter.Skip{Resource: x.Resource, Reason: adapter.Unsupported,
					Detail: "running the tool's own command is not supported yet"})
				continue
			}
			tp.ops = append(tp.ops, op)
		}
		for _, sk := range tp.info.Skips {
			tp.held[sk.Resource.Code] = true
		}
	}

	p.shared = dedupe(tps)

	ex, err := e.executorOn(p.extra, nil)
	if err != nil {
		return nil, err
	}
	defer ex.Close()
	var reverts, writes []Item
	for _, tp := range tps {
		if tp.info.Status == ToolPlanned {
			w, r, status, reason := e.compare(ex, tp, m, st, opt)
			if status != ToolPlanned {
				tp.info.Status, tp.info.Reason = status, reason
			} else {
				writes = append(writes, w...)
				reverts = append(reverts, r...)
				p.planned = append(p.planned, tp.t.id())
				for _, r := range tp.t.reads.paths {
					if strings.HasPrefix(r, "~/") && !slices.Contains(p.touches, r) {
						p.touches = append(p.touches, r)
					}
				}
			}
		}
		tp.info.Held = heldList(tp.held)
		p.Tools = append(p.Tools, tp.info)
	}
	// Undo newest first, across tools, in the order the manifest recorded the writes.
	order := map[string]int{}
	for i, en := range m.Entries {
		order[en.ID] = i
	}
	slices.SortStableFunc(reverts, func(a, b Item) int { return cmp.Compare(order[b.ID], order[a.ID]) })
	p.Items = append(reverts, writes...)
	if err := e.consents(p); err != nil {
		return nil, err
	}
	return p, nil
}

func (e *Engine) newPlan(m *manifest.Manifest, tools []*tool) *Plan {
	p := &Plan{
		fingerprint: fingerprint(m),
		extra:       e.extraRoots(tools),
		roots:       map[string]map[string]string{},
		credentials: e.credentials(),
	}
	for _, t := range tools {
		p.roots[t.id()] = t.roots
	}
	return p
}

func heldList(held map[string]bool) []string {
	var out []string
	for code, h := range held {
		if h && code != "" {
			out = append(out, code)
		}
	}
	slices.Sort(out)
	return out
}

// compare goes through the table for one tool: every target of its plan, and every entry of the
// manifest the plan no longer has. A target that cannot be read leaves the whole tool out.
func (e *Engine) compare(ex Executor, tp *toolPlan, m *manifest.Manifest, st *desired.State, opt Options) (writes, reverts []Item, status ToolStatus, reason string) {
	id := tp.t.id()
	planned := map[string]bool{}
	for _, op := range tp.ops {
		target := executor.TargetOf(op)
		itemID := manifest.EntryID(id, target.Op, target.Path, target.Key)
		if planned[itemID] {
			return nil, nil, ToolFailed, fmt.Sprintf("the adapter planned %s twice", itemID)
		}
		planned[itemID] = true
		want, err := executor.Expect(op)
		if err != nil {
			return nil, nil, ToolFailed, fmt.Sprintf("%s: %v", target.Path, err)
		}
		obs := ex.Observe(target)
		if obs.State == executor.Unknown {
			return nil, nil, ToolUnreadable, fmt.Sprintf("%s cannot be read: %v", target.Path, obs.Err)
		}
		it := Item{Tool: id, Resource: op.Ref(), Target: target, ID: itemID, Op: op, Observed: obs, Want: want,
			Global: target.Op != manifest.OpTree && target.Op != manifest.OpFile, SharedWith: tp.sharedWith(itemID)}
		for _, d := range tp.info.Degraded {
			if d.Resource.Code == it.Resource.Code {
				it.Degraded = d.Note
			}
		}
		en, has := m.Find(itemID)
		if has {
			it.Entry = &en
		}
		overwrite := slices.Contains(opt.Overwrite, it.Resource.Code)
		switch {
		case !has && obs.State == executor.Absent:
			it.Action = ActionCreate
		case !has:
			it.Action = ActionTaken
		case obs.State == executor.Absent && overwrite:
			it.Action = ActionRestore
		case obs.State == executor.Absent:
			it.Action = ActionDeleted
		case obs.Digest == en.Digest && want == en.Digest:
			it.Action = ActionNone
			if en.Version != it.Resource.Version || !slices.Equal(en.SharedWith, it.SharedWith) {
				it.Action = ActionRecord
			}
		case obs.Digest == want:
			it.Action = ActionRecord
		case obs.Digest == en.Digest:
			it.Action = ActionUpdate
		case overwrite:
			it.Action = ActionOverwrite
		default:
			it.Action = ActionModified
		}
		writes = append(writes, it)
	}
	for _, en := range m.Entries {
		if en.Tool != id || planned[en.ID] || tp.held[en.Resource] {
			continue
		}
		target := executor.TargetOfEntry(en)
		obs := ex.Observe(target)
		if obs.State == executor.Unknown {
			return nil, nil, ToolUnreadable, fmt.Sprintf("%s cannot be read: %v", en.Path, obs.Err)
		}
		reverts = append(reverts, revertItem(en, obs, st))
	}
	return writes, reverts, ToolPlanned, ""
}

// revertItem is the item that undoes an entry.
func revertItem(en manifest.Entry, obs executor.Observed, st *desired.State) Item {
	ref := resource.Ref{Code: en.Resource, Version: en.Version}
	if st != nil {
		for _, x := range st.Excluded {
			if x.Ref.Code == en.Resource {
				ref.Kind = x.Ref.Kind
			}
		}
	}
	return Item{
		Tool: en.Tool, Resource: ref, Action: ActionRevert, Target: executor.TargetOfEntry(en), ID: en.ID,
		Entry: &en, Observed: obs, Global: en.Op != manifest.OpTree && en.Op != manifest.OpFile,
		SharedWith: slices.Clone(en.SharedWith),
	}
}
