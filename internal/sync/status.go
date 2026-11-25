// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"context"
	"slices"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/executor"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// EntryState is the state of one manifest entry. The values are stable names for
// machine-readable output.
type EntryState string

const (
	// Synced: on disk as written, and as wanted.
	Synced EntryState = "synced"
	// Outdated: on disk as written; the source has something newer (a sync updates it).
	Outdated EntryState = "outdated"
	// Modified: changed on this computer since it was written (drift). A sync does not
	// overwrite it without --overwrite.
	Modified EntryState = "modified"
	// Deleted: gone from this computer since it was written (drift).
	Deleted EntryState = "deleted"
	// Unreadable: it could not be looked at; its state is unknown.
	Unreadable EntryState = "unreadable"
	// Removing: no longer wanted; a sync undoes it.
	Removing EntryState = "removing"
)

// StatusEntry is one manifest entry and what it looks like now.
type StatusEntry struct {
	Entry    manifest.Entry
	State    EntryState
	Observed executor.Observed
	// Want is the operation the source wants at the target, when there is a desired state and
	// the tool took part: what status --diff compares the disk with.
	Want adapter.Op
}

// Status is what was written, where, and how it compares with the disk and the source.
type Status struct {
	// Tools is set when a desired state was given.
	Tools []Tool
	// Entries follow the manifest's order.
	Entries []StatusEntry
	// Pending are the targets the source wants that are not there yet, and the targets whose
	// name the person's own things already have.
	Pending []Item
}

// Drift reports whether any entry was changed or deleted on this computer, or could not be read.
func (s *Status) Drift() bool {
	return slices.ContainsFunc(s.Entries, func(e StatusEntry) bool {
		return e.State == Modified || e.State == Deleted || e.State == Unreadable
	})
}

// Differs reports whether a sync would change anything (status --exit-code).
func (s *Status) Differs() bool {
	return len(s.Pending) > 0 || slices.ContainsFunc(s.Entries, func(e StatusEntry) bool { return e.State != Synced })
}

// Status looks at every manifest entry without changing anything. With a desired state it also
// says which entries a sync would update or remove and what is still to come; without one (no
// repository configured, or it cannot be reached) it only compares the disk with the manifest.
func (e *Engine) Status(ctx context.Context, st *desired.State, opt Options) (*Status, error) {
	out := &Status{}
	byID := map[string]Item{}
	if st != nil {
		p, err := e.Plan(ctx, st, opt)
		if err != nil {
			return nil, err
		}
		out.Tools = p.Tools
		for _, it := range p.Items {
			byID[it.ID] = it
			if it.Action == ActionCreate || it.Action == ActionTaken {
				out.Pending = append(out.Pending, it)
			}
		}
	}
	m, err := e.store.Read()
	if err != nil {
		return nil, err
	}
	tools, err := e.detect(ctx)
	if err != nil {
		return nil, err
	}
	ex, err := e.executorOn(e.extraRoots(tools), nil)
	if err != nil {
		return nil, err
	}
	defer ex.Close()
	for _, en := range m.Entries {
		se := StatusEntry{Entry: en}
		if it, ok := byID[en.ID]; ok {
			se.Observed, se.Want = it.Observed, it.Op
			switch it.Action {
			case ActionNone, ActionRecord:
				se.State = Synced
			case ActionUpdate:
				se.State = Outdated
			case ActionModified, ActionOverwrite:
				se.State = Modified
			case ActionDeleted, ActionRestore:
				se.State = Deleted
			case ActionRevert:
				se.State = Removing
			}
			out.Entries = append(out.Entries, se)
			continue
		}
		// The tool did not take part, or the resource was held: what the disk says.
		se.Observed = ex.Observe(executor.TargetOfEntry(en))
		switch {
		case se.Observed.State == executor.Unknown:
			se.State = Unreadable
		case se.Observed.State == executor.Absent:
			se.State = Deleted
		case se.Observed.Digest == en.Digest:
			se.State = Synced
		default:
			se.State = Modified
		}
		out.Entries = append(out.Entries, se)
	}
	return out, nil
}
