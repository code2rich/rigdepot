// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"cmp"
	"context"
	"runtime"
	"slices"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/sync"
)

// The reasons a report gives for a resource that is not installed in a tool, besides the
// adapters' skip reasons (adapter.SkipReason). Stable codes; never a path or a message.
const (
	reasonConsent    = "consent_needed"        // a global change waits for the person
	reasonChanged    = "changed_while_syncing" // another program changed the target
	reasonTaken      = "name_taken"            // something of the person's is there
	reasonModified   = "modified_locally"      // the person changed what was written
	reasonDeleted    = "deleted_locally"       // the person deleted what was written
	reasonUnknown    = "unavailable"           // the resource could not be had or read
	reasonUnreadable = "config_unreadable"     // the tool's configuration cannot be read
	reasonToolFailed = "tool_failed"           // the adapter could not plan
)

// installStatus ranks what a sync left of a resource in a tool: the worst of its targets wins.
var installStatus = map[apiclient.InstallStatus]int{
	apiclient.InstallStatusRemoved: 0, apiclient.InstallStatusInstalled: 1, apiclient.InstallStatusSkipped: 2, apiclient.InstallStatusFailed: 3,
}

// buildReport is what a sync tells the server: which tools it found and, per resource and tool,
// the version and whether it is installed, skipped, failed or removed, with a stable reason
// code. It is made of identifiers, versions and codes only. A plan item's target, the details of
// a skip and error messages, which can hold paths, never go into it.
func buildReport(st *desired.State, p *sync.Plan, res *sync.Result, failed, pending bool, finished time.Time) apiclient.SyncReport {
	r := apiclient.SyncReport{
		CliVersion: apiclient.Version(), Os: apiclient.SyncReportOs(runtime.GOOS), Arch: runtime.GOARCH,
		FinishedAt: finished.UTC(), Result: apiclient.SyncResultOk, Tools: []apiclient.DetectedTool{}, Installs: []apiclient.InstallReport{},
	}
	switch {
	case failed:
		r.Result = apiclient.SyncResultFailed
	case pending:
		r.Result = apiclient.SyncResultPartial
	}
	versions := map[string]int{}
	for _, res := range st.Resources {
		versions[res.Ref.Code] = res.Ref.Version
	}
	// The single entry is where the MCP servers behind it are: what happens to it happens to
	// each of them.
	behind := map[string][]string{}
	for _, t := range p.Tools {
		for _, ref := range t.BehindEntry {
			behind[t.ID] = append(behind[t.ID], ref.Code)
		}
	}
	type key struct{ tool, code string }
	got := map[key]apiclient.InstallReport{}
	var put func(tool, code string, version int, status apiclient.InstallStatus, reason, degraded string)
	put = func(tool, code string, version int, status apiclient.InstallStatus, reason, degraded string) {
		if _, real := versions[code]; !real && code == sync.SingleEntry.Code {
			for _, c := range behind[tool] {
				put(tool, c, 0, status, reason, degraded)
			}
			return
		}
		if v, ok := versions[code]; ok && version == 0 {
			version = v
		}
		if version < 1 {
			return // from a repository, or older than the server: nothing the server knows
		}
		k := key{tool, code}
		if prev, ok := got[k]; ok && installStatus[prev.Status] > installStatus[status] {
			return
		}
		in := apiclient.InstallReport{Code: code, Version: version, Tool: tool, Status: status}
		if reason != "" {
			in.Reason = &reason
		}
		if degraded != "" {
			yes, form := true, "skill" // every fallback form is a skill
			in.Degraded, in.Form = &yes, &form
		}
		got[k] = in
	}

	done := map[string]bool{}
	for _, d := range res.Done {
		done[d.Item.ID] = true
	}
	deferred := map[string]bool{}
	for _, it := range res.Deferred {
		deferred[it.ID] = true
	}
	retry := map[string]bool{}
	for _, f := range res.Retry {
		retry[f.Item.ID] = true
	}
	for _, it := range p.Items {
		code, ver := it.Resource.Code, it.Resource.Version
		switch {
		case it.Action == sync.ActionRevert:
			if done[it.ID] {
				put(it.Tool, code, ver, apiclient.InstallStatusRemoved, "", "")
			}
		case retry[it.ID]:
			put(it.Tool, code, ver, apiclient.InstallStatusFailed, reasonChanged, it.Degraded)
		case deferred[it.ID]:
			put(it.Tool, code, ver, apiclient.InstallStatusSkipped, reasonConsent, it.Degraded)
		case it.Action == sync.ActionTaken:
			put(it.Tool, code, ver, apiclient.InstallStatusSkipped, reasonTaken, it.Degraded)
		case it.Action == sync.ActionModified:
			put(it.Tool, code, ver, apiclient.InstallStatusSkipped, reasonModified, it.Degraded)
		case it.Action == sync.ActionDeleted:
			put(it.Tool, code, ver, apiclient.InstallStatusSkipped, reasonDeleted, it.Degraded)
		case it.Action.Writes() && !done[it.ID]:
			// The run stopped before it got here.
			put(it.Tool, code, ver, apiclient.InstallStatusFailed, reasonToolFailed, it.Degraded)
		default:
			put(it.Tool, code, ver, apiclient.InstallStatusInstalled, "", it.Degraded)
		}
	}
	for _, t := range p.Tools {
		if t.Detection.Status == adapter.Installed {
			dt := apiclient.DetectedTool{Tool: t.ID}
			if t.Detection.Version != "" {
				v := t.Detection.Version
				dt.Version = &v
			}
			r.Tools = append(r.Tools, dt)
		}
		switch t.Status {
		case sync.ToolPlanned:
			for _, sk := range t.Skips {
				put(t.ID, sk.Resource.Code, sk.Resource.Version, apiclient.InstallStatusSkipped, string(sk.Reason), "")
			}
			for _, f := range st.Failed {
				put(t.ID, f.Ref.Code, f.Ref.Version, apiclient.InstallStatusSkipped, reasonUnknown, "")
			}
		case sync.ToolUnreadable, sync.ToolFailed:
			reason := reasonUnreadable
			if t.Status == sync.ToolFailed {
				reason = reasonToolFailed
			}
			for _, res := range st.Resources {
				put(t.ID, res.Ref.Code, res.Ref.Version, apiclient.InstallStatusSkipped, reason, "")
			}
		}
	}
	for _, in := range got {
		r.Installs = append(r.Installs, in)
	}
	slices.SortFunc(r.Installs, func(a, b apiclient.InstallReport) int {
		return cmp.Or(cmp.Compare(a.Code, b.Code), cmp.Compare(a.Tool, b.Tool))
	})
	return r
}

// report tells the server what the sync left on this computer. A failure is only told: the
// sync itself is done, and the next one reports again.
func (a *app) report(ctx context.Context, rem *remote, st *desired.State, p *sync.Plan, res *sync.Result, failed, pending bool) {
	body := buildReport(st, p, res, failed, pending, time.Now())
	resp, err := rem.client.ReportSyncWithResponse(ctx, body)
	if err == nil {
		err = apiclient.Check(resp.HTTPResponse, resp.Body)
	}
	if err != nil {
		a.out.Warn(a.p.T(msg.RemoteReportFailed, i18n.V("address", rem.creds.Server), i18n.V("detail", err.Error())))
	}
}

// newerHint says, once a sync is done, that the server hands out a newer command line.
func (a *app) newerHint(rem *remote) {
	org := rem.state.Org
	if org.LatestVersion != "" && desired.Older(apiclient.Version(), org.LatestVersion) {
		a.out.Warn(a.p.T(msg.RemoteNewer, i18n.V("latest", org.LatestVersion), i18n.V("version", apiclient.Version())))
	}
}
