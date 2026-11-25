// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"context"
	"fmt"
	"slices"

	"github.com/code2rich/rigdepot/internal/executor"
)

// UninstallOptions change what an uninstall covers.
type UninstallOptions struct {
	// Tools limits the uninstall to these tools (--tool). Empty means every entry of the
	// manifest, including those of tools this program no longer has an adapter for.
	Tools []string
}

// PlanUninstall plans undoing what the manifest records, newest entry first. Only manifest
// entries are undone; nothing else with the product's prefix is touched.
//
// A tool with a target that cannot be read is left out entirely, like in a sync. An entry
// another tool reads as well (Item.SharedWith) takes the resource away from that tool too, until
// its next sync; the command line points that out.
func (e *Engine) PlanUninstall(ctx context.Context, opt UninstallOptions) (*Plan, error) {
	if err := e.checkTools(opt.Tools); err != nil {
		return nil, err
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
	p.Uninstall = true
	ex, err := e.executorOn(p.extra, nil)
	if err != nil {
		return nil, err
	}
	defer ex.Close()

	unreadable := map[string]string{}
	var items []Item
	for i := len(m.Entries) - 1; i >= 0; i-- {
		en := m.Entries[i]
		if len(opt.Tools) > 0 && !slices.Contains(opt.Tools, en.Tool) {
			continue
		}
		obs := ex.Observe(executor.TargetOfEntry(en))
		if obs.State == executor.Unknown {
			if _, seen := unreadable[en.Tool]; !seen {
				unreadable[en.Tool] = fmt.Sprintf("%s cannot be read: %v", en.Path, obs.Err)
			}
			continue
		}
		items = append(items, revertItem(en, obs, nil))
	}
	for _, it := range items {
		if _, skip := unreadable[it.Tool]; !skip {
			p.Items = append(p.Items, it)
		}
	}
	for _, t := range tools {
		info := Tool{ID: t.id(), Name: t.profile.Name, Detection: t.det, Status: ToolPlanned}
		switch {
		case len(opt.Tools) > 0 && !slices.Contains(opt.Tools, t.id()):
			info.Status = ToolNotSelected
		case unreadable[t.id()] != "":
			info.Status, info.Reason = ToolUnreadable, unreadable[t.id()]
		}
		p.Tools = append(p.Tools, info)
	}
	// Tools in the manifest without an adapter: their entries are undone all the same.
	for id, reason := range unreadable {
		if !slices.ContainsFunc(p.Tools, func(t Tool) bool { return t.ID == id }) {
			p.Tools = append(p.Tools, Tool{ID: id, Status: ToolUnreadable, Reason: reason})
		}
	}
	if err := e.consents(p); err != nil {
		return nil, err
	}
	return p, nil
}
