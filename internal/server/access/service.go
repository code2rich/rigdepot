// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access

import (
	"context"
	"errors"
	"time"

	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Auditor appends to the audit log inside the caller's transaction: when the
// entry cannot be written, the transaction rolls back and the operation
// fails (docs/subsystems/audit-confirm.md#失败与兼容). Package audit
// implements it.
type Auditor interface {
	Append(ctx context.Context, tx *dbx.Tx, e AuditEntry) error
}

// AuditEntry is one line of the audit log as this package describes it. The
// audit package adds the time, the sequence number, the category
// ("platform") and the hash chain.
type AuditEntry struct {
	Actor       Actor
	Action      Action
	ObjectKind  string         // product, membership, join_request, user, invite
	ObjectID    string         // the object's id
	ObjectLabel string         // its name at the time, readable after it is renamed
	Change      map[string]any // what changed, summarised; never secrets or content
	Result      AuditResult
	Reason      string // for denials: one of the Reason constants
}

// AuditResult is how an audited operation ended.
type AuditResult string

// The results this package records.
const (
	AuditOK     AuditResult = "ok"
	AuditDenied AuditResult = "denied"
)

// Event is a domain event: something that happened, for notifications and
// modules to react to. Events are published after the transaction commits.
type Event interface{ EventName() string }

// Publisher hands events to their subscribers. Package events implements
// it. It is called after the change is committed and cannot undo it.
type Publisher interface {
	Publish(ctx context.Context, e Event)
}

// Service is the access domain: accounts, products, membership, join
// requests, invitations and the permission decisions every other domain
// package asks for.
type Service struct {
	db     *dbx.DB
	audit  Auditor
	events Publisher
	now    func() time.Time
}

// Options are what a Service needs besides the database.
type Options struct {
	Auditor Auditor          // required
	Events  Publisher        // nil drops events
	Now     func() time.Time // nil is time.Now
}

// New returns the access service on db. It panics without an Auditor: no
// write may happen without its audit entry.
func New(db *dbx.DB, o Options) *Service {
	if o.Auditor == nil {
		panic("access: New needs an Auditor")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{db: db, audit: o.Auditor, events: o.Events, now: o.Now}
}

// object names what an operation works on, for the audit log.
type object struct {
	kind, id, label string
}

// writeOp is one write operation in progress: its transaction, the audit
// entries it made and the events to publish once it has committed.
type writeOp struct {
	s      *Service
	ctx    context.Context
	tx     *dbx.Tx
	actor  Actor
	events []Event
	denied *AuditEntry
}

// write runs fn in a transaction. fn calls op.check before it changes
// anything and op.record for every change; a denial is recorded in the audit
// log in a transaction of its own after fn's has rolled back.
func (s *Service) write(ctx context.Context, a Actor, fn func(op *writeOp) error) error {
	var op *writeOp
	err := s.db.InTx(ctx, func(tx *dbx.Tx) error {
		op = &writeOp{s: s, ctx: ctx, tx: tx, actor: a}
		return fn(op)
	})
	if op != nil && op.denied != nil {
		if auditErr := s.db.InTx(ctx, func(tx *dbx.Tx) error {
			return s.audit.Append(ctx, tx, *op.denied)
		}); auditErr != nil {
			return errors.Join(err, auditErr)
		}
	}
	if err != nil {
		return err
	}
	if s.events != nil {
		for _, e := range op.events {
			s.events.Publish(ctx, e)
		}
	}
	return nil
}

// check returns ErrDenied unless the actor may do action to t. A denied
// write is remembered so that write can record it.
func (op *writeOp) check(action Action, t Target, obj object) error {
	d := op.s.CanIn(op.ctx, op.tx, op.actor, action, t)
	if d.Err != nil {
		return d.Err
	}
	if d.Allowed {
		return nil
	}
	if action.IsWrite() {
		op.denied = &AuditEntry{
			Actor: op.actor, Action: action, Result: AuditDenied, Reason: d.Reason,
			ObjectKind: obj.kind, ObjectID: obj.id, ObjectLabel: obj.label,
		}
	}
	return ErrDenied
}

// record writes the audit entry of a change in the operation's transaction.
func (op *writeOp) record(action Action, obj object, change map[string]any) error {
	if change == nil {
		change = map[string]any{}
	}
	return op.s.audit.Append(op.ctx, op.tx, AuditEntry{
		Actor: op.actor, Action: action, Result: AuditOK, Change: change,
		ObjectKind: obj.kind, ObjectID: obj.id, ObjectLabel: obj.label,
	})
}

// emit queues an event for after the commit.
func (op *writeOp) emit(e Event) { op.events = append(op.events, e) }

// read checks a read-only action outside a transaction.
func (s *Service) read(ctx context.Context, a Actor, action Action, t Target) error {
	d := s.Can(ctx, a, action, t)
	if d.Err != nil {
		return d.Err
	}
	if !d.Allowed {
		return ErrDenied
	}
	return nil
}

func (s *Service) millis() int64 { return dbx.Millis(s.now()) }
