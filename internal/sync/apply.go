// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/executor"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// Session is the engine while this process holds the cross-process lock. Everything that changes
// the manifest or a tool happens in a session.
type Session struct {
	e      *Engine
	locked *manifest.Locked
	// Recovery is what Lock found in the intent log and did about it: the writes of a program
	// that did not finish. Unresolved intents stay for the doctor command to list.
	Recovery manifest.Recovery
}

// Lock takes the lock, waiting up to Config.LockWait, and settles what an earlier run that did
// not finish left behind: the executor puts half-done writes right (Settle) and the intents are
// committed or dropped (manifest.Locked.Recover).
//
// When another program holds the lock, the error satisfies manifest.ErrLocked and
// manifest.ErrUnknown (exit code 9).
func (e *Engine) Lock(ctx context.Context) (*Session, error) {
	l, err := e.store.Lock(ctx, e.wait)
	if err != nil {
		return nil, err
	}
	s := &Session{e: e, locked: l}
	tools, err := e.detect(ctx)
	if err != nil {
		l.Release()
		return nil, err
	}
	ex, err := e.executorOn(e.extraRoots(tools), l)
	if err != nil {
		l.Release()
		return nil, err
	}
	defer ex.Close()
	if s.Recovery, err = l.Recover(ex.Settle); err != nil {
		l.Release()
		return nil, err
	}
	return s, nil
}

// Release lets go of the lock.
func (s *Session) Release() error { return s.locked.Release() }

// ErrStalePlan means the manifest changed after the plan was made; plan again.
var ErrStalePlan = errors.New("the manifest changed after the plan was made")

// Result is what Apply did.
type Result struct {
	// Done are the items carried out: written, undone, or recorded in the manifest only.
	Done []Done
	// Deferred are the global changes the person did not agree to; they wait for a run in
	// which they do.
	Deferred []Item
	// Retry are the items whose target changed between the plan and the write (another program
	// rewrote the file); nothing was written to them.
	Retry []Failure
}

// Done is one item carried out.
type Done struct {
	Item Item
	// Kept is where a changed target was copied before it was replaced or removed, relative to
	// the local data directory, or "".
	Kept string
	// Outcome is what an undo found, for ActionRevert.
	Outcome executor.Outcome
	// Leftover is a directory moved aside that could not be removed yet, or "".
	Leftover string
}

// Failure is an item that was not carried out.
type Failure struct {
	Item Item
	Err  error
}

// Attention reports whether something is left for the person: global changes not agreed to, or
// targets that changed while the run was going on (exit code 3).
func (r *Result) Attention() bool { return len(r.Deferred) > 0 || len(r.Retry) > 0 }

// Apply carries out a plan on the real home directory, in its order: undos first, then writes.
// Items the approval does not cover are deferred. A target that changed since the plan was made
// is not written and is reported in Result.Retry. Any other failure stops the run: what was done
// so far is in the manifest, and the error is returned together with the result.
//
// The manifest must still be the one the plan saw, or the error is ErrStalePlan and nothing is
// done.
func (s *Session) Apply(ctx context.Context, p *Plan, ap Approval) (*Result, error) {
	m, err := s.locked.Read()
	if err != nil {
		return nil, err
	}
	if fingerprint(m) != p.fingerprint {
		return nil, ErrStalePlan
	}
	e := s.e
	ex, err := e.executorOn(p.extra, s.locked)
	if err != nil {
		return nil, err
	}
	defer ex.Close()

	res := &Result{}
	var runErr error
	for _, it := range p.Items {
		if err := ctx.Err(); err != nil {
			runErr = err
			break
		}
		if !it.Action.Changes() && it.Action != ActionRecord {
			continue
		}
		if it.Action == ActionRecord { // only the manifest changes; no consent needed
			res.Done = append(res.Done, Done{Item: it})
			continue
		}
		if !ap.Approved(it) {
			res.Deferred = append(res.Deferred, it)
			continue
		}
		var done Done
		switch {
		case it.Action == ActionRevert:
			r, err := ex.Revert(ctx, *it.Entry)
			if err != nil {
				if errors.Is(err, executor.ErrUnreadable) {
					res.Retry = append(res.Retry, Failure{Item: it, Err: err})
					continue
				}
				runErr = fmt.Errorf("undoing %s: %w", it.ID, err)
			}
			done = Done{Item: it, Kept: r.Kept, Outcome: r.Outcome, Leftover: r.Leftover}
		default:
			tool := executor.Tool{ID: it.Tool, Roots: p.roots[it.Tool]}
			a, err := write(ctx, ex, tool, it, it.Op, it.Entry, it.Observed)
			if err != nil {
				if errors.Is(err, executor.ErrChanged) || errors.Is(err, executor.ErrUnreadable) {
					res.Retry = append(res.Retry, Failure{Item: it, Err: err})
					continue
				}
				runErr = fmt.Errorf("writing %s: %w", it.ID, err)
			}
			done = Done{Item: it, Kept: a.Kept, Leftover: a.Leftover}
		}
		if runErr != nil {
			break
		}
		res.Done = append(res.Done, done)
	}

	// What only the manifest records: digests changed to the wanted ones, which tools share an
	// entry, where the resources came from. Done even after a failure, for what was done.
	if err := s.record(p, res); err != nil {
		runErr = errors.Join(runErr, err)
	}
	if err := e.remember(ap.remember); err != nil && !errors.Is(err, errConsentUnknown) {
		runErr = errors.Join(runErr, err)
	}
	return res, runErr
}

// replacesKey reports whether an item gives a key we set before a new value.
func replacesKey(it Item) bool {
	return it.Target.Op == manifest.OpKey && it.Entry != nil && it.Observed.State == executor.Present &&
		(it.Action == ActionUpdate || it.Action == ActionOverwrite)
}

// write carries out an item that writes, with op and en as the executor sees them (the preview
// maps them into its sandbox) and pre what the target looked like.
//
// A key we set before is not given its new value in place: it is undone, then set again. Setting
// a key that is already there replaces its value and records no edit, so an in-place update
// would leave an entry that uninstall can only remove by its meaning, no longer byte for byte;
// undoing it first takes out exactly the bytes the first write added, and the new write records
// its own. A value the person changed is kept by the undo, as an overwrite promises.
func write(ctx context.Context, ex Executor, tool executor.Tool, it Item, op adapter.Op, en *manifest.Entry, pre executor.Observed) (executor.Applied, error) {
	if !replacesKey(it) {
		return ex.Apply(ctx, tool, op, pre, executor.Options{Keep: it.Action == ActionOverwrite})
	}
	target := executor.TargetOf(op)
	if cur := ex.Observe(target); !cur.Same(pre) {
		return executor.Applied{}, fmt.Errorf("%s: %w", it.ID, executor.ErrChanged)
	}
	r, err := ex.Revert(ctx, *en)
	if err != nil {
		return executor.Applied{}, err
	}
	a, err := ex.Apply(ctx, tool, op, ex.Observe(target), executor.Options{})
	if a.Kept == "" {
		a.Kept = r.Kept
	}
	return a, err
}

// record updates the manifest for what Apply did not write through the executor.
func (s *Session) record(p *Plan, res *Result) error {
	m, err := s.locked.Read()
	if err != nil {
		return err
	}
	changed := false
	for _, d := range res.Done {
		it := d.Item
		if !it.Action.Writes() && it.Action != ActionRecord {
			continue
		}
		en, ok := m.Find(it.ID)
		if !ok {
			continue
		}
		upd := en
		if it.Action == ActionRecord {
			upd.Digest, upd.Version, upd.At = it.Want, it.Resource.Version, s.e.now().UTC()
		}
		upd.SharedWith = slices.Clone(it.SharedWith)
		if upd.Digest != en.Digest || upd.Version != en.Version || !slices.Equal(upd.SharedWith, en.SharedWith) {
			m.Put(upd)
			changed = true
		}
	}
	if p.Source != nil && p.Source.Type != "" && (m.Source == nil || m.Source.Type != p.Source.Type || m.Source.URL != p.Source.URL) {
		m.Source = &manifest.Source{Type: p.Source.Type, URL: p.Source.URL}
		changed = true
	}
	if !changed {
		return nil
	}
	return s.locked.Write(m)
}
