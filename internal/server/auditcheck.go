// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/code2rich/rigdepot/internal/server/events"
	"github.com/code2rich/rigdepot/internal/server/jobs"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// The daily check of the audit chain (docs/architecture/06-server.md, background jobs): each
// run checks the rows added since the last one, from the row after the last one found intact
// and anchored at its hash, so that a row changed, removed or inserted since, and an end of the
// log cut off, are noticed. The outcome is kept in the settings; a broken chain makes /readyz
// report "audit_chain" as degraded and the administrators are notified (events.AuditChainBroken),
// every day until the log is restored from a backup.
// "audit verify" checks the whole chain on demand.

// auditCheckJob is the job's name.
const auditCheckJob = "audit-verify"

const (
	auditCheckEvery   = 24 * time.Hour
	auditCheckTimeout = 30 * time.Minute
)

// errAuditBroken is what the job returns when the chain is broken, so that it counts as failed.
var errAuditBroken = errors.New("the audit log was tampered with")

// checkAudit runs one check of the audit chain and keeps its outcome.
func (a *App) checkAudit(ctx context.Context) error {
	prev, found, err := a.settings.AuditCheck(ctx)
	if err != nil {
		return err
	}
	from, anchor := int64(1), ""
	if found {
		from, anchor = prev.LastSeq+1, prev.LastHash
	}
	rep, err := a.audit.Verify(ctx, from, anchor)
	if err != nil {
		return err
	}
	next := settings.AuditCheck{LastSeq: prev.LastSeq, LastHash: prev.LastHash, At: time.Now().UTC()}
	if rep.Broken != nil {
		// Stay at the last row known to be intact: the next run finds the same break.
		next.Broken, next.BrokenSeq = rep.Broken.Problem, rep.Broken.Seq
	} else if rep.Checked > 0 {
		next.LastSeq, next.LastHash = rep.LastSeq, rep.LastHash
	}
	if err := a.settings.SaveAuditCheck(ctx, next); err != nil {
		return err
	}
	if rep.Broken != nil {
		if a.bg != nil {
			// notify tells the administrators, every day until the log is restored.
			a.bg.Events.PublishCommitted(events.AuditChainBroken{Seq: rep.Broken.Seq, IntactThrough: next.LastSeq, Problem: rep.Broken.Problem})
		}
		a.log.Error("the audit log was tampered with: restore it from a backup and find out who could write to the database",
			"seq", rep.Broken.Seq, "problem", rep.Broken.Problem, "intact_through", next.LastSeq)
		return fmt.Errorf("%w: %v", errAuditBroken, rep.Broken)
	}
	a.log.Info("audit log checked", "rows", rep.Checked, "last_seq", next.LastSeq)
	return nil
}

// auditChainReady is the readiness item "audit_chain": degraded when the last check found the
// chain broken, or when the last run of the check did not finish.
func (a *App) auditChainReady(ctx context.Context) error {
	c, found, err := a.settings.AuditCheck(ctx)
	if err != nil {
		return err
	}
	if found && c.Broken != "" {
		return fmt.Errorf("%w at #%d: %s", errAuditBroken, c.BrokenSeq, c.Broken)
	}
	if a.bg == nil {
		return nil
	}
	statuses, err := a.bg.Jobs.Statuses(ctx)
	if err != nil {
		return err
	}
	for _, s := range statuses {
		if s.Name == auditCheckJob && (s.LastResult == jobs.Failed || s.LastResult == jobs.TimedOut) {
			return fmt.Errorf("the last check of the audit log did not finish (%s): %s", s.LastResult, s.LastError)
		}
	}
	return nil
}

// addAuditCheck schedules the check and adds its readiness item.
func (a *App) addAuditCheck() error {
	if err := a.bg.Jobs.Add(jobs.Job{Name: auditCheckJob, Every: auditCheckEvery, Timeout: auditCheckTimeout, Run: a.checkAudit}); err != nil {
		return err
	}
	a.AddReadyCheck(ReadyCheck{Name: "audit_chain", Check: a.auditChainReady})
	return nil
}
