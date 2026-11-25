// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/internal/check"
	"github.com/code2rich/rigdepot/internal/repoformat"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/events"
)

// Auditor writes the audit log. *audit.Log implements it: Append inside the transaction of the
// change, Record for a refusal, which has no transaction of its own.
type Auditor interface {
	Append(ctx context.Context, tx *dbx.Tx, e audit.Entry) (int64, error)
	Record(ctx context.Context, e audit.Entry) (int64, error)
}

// Events receives the catalog's events, delivered once the change has committed. *events.Bus
// implements it.
type Events interface {
	Publish(tx events.Tx, p events.Payload)
}

// Options are what a Service needs besides the database and the access service.
type Options struct {
	Auditor Auditor // required
	Events  Events  // nil drops events
	// Kinds are the resource kinds; nil is the program's registry (builtin/all).
	Kinds repoformat.Kinds
	// Rules is the rule set every resource is checked against; the zero value is the
	// program's (extcheck.DefaultRuleset). rgd check uses the same.
	Rules extcheck.Ruleset
	// Version is the server's version, checked against a repository's requires.rgd.
	Version string
	Now     func() time.Time // nil is time.Now
}

// Service is the catalog: resources, drafts, versions, publishing, rollback, disabling,
// references and impact, check results and waivers.
type Service struct {
	db        *dbx.DB
	access    *access.Service
	audit     Auditor
	events    Events
	kinds     repoformat.Kinds
	engine    *check.Engine
	engineErr error
	rules     extcheck.Ruleset
	ver       string
	now       func() time.Time
}

// New returns the catalog on db. It fails without an Auditor: no write may happen without its
// audit entry.
func New(db *dbx.DB, acc *access.Service, o Options) (*Service, error) {
	if o.Auditor == nil {
		return nil, errors.New("catalog: New needs an Auditor")
	}
	if o.Rules.Version == 0 && o.Rules.Rules == nil {
		o.Rules = extcheck.DefaultRuleset()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Service{db: db, access: acc, audit: o.Auditor, events: o.Events, kinds: o.Kinds, rules: o.Rules, ver: o.Version, now: o.Now}
	// A program without a rule set (built without builtin/all) still serves what is
	// published; it cannot take new content, because nothing could be checked.
	s.engine, s.engineErr = check.New(o.Rules)
	if s.engineErr != nil {
		s.engineErr = fmt.Errorf("catalog: cannot check new content: %w", s.engineErr)
	}
	return s, nil
}

// checker returns the check engine, or why there is none.
func (s *Service) checker() (*check.Engine, error) { return s.engine, s.engineErr }

// RulesetVersion is the version of the rule set resources are checked against now.
func (s *Service) RulesetVersion() int { return s.rules.Version }

func (s *Service) millis() int64 { return dbx.Millis(s.now()) }

// auditActor is an access.Actor as the audit log records it.
func auditActor(a access.Actor) audit.Actor {
	return audit.Actor{UserID: a.UserID, Origin: audit.Origin(a.Origin), ViaTool: a.ViaTool, DeviceID: a.DeviceID}
}

// op is one write operation in progress: its transaction, the events to publish and, when it
// was refused, the refusal to record.
type op struct {
	s      *Service
	ctx    context.Context
	tx     *dbx.Tx
	actor  access.Actor
	denied *audit.Entry
}

// write runs fn in a transaction. fn calls op.check before it changes anything and op.record
// for the change; a refusal is recorded in a transaction of its own after fn's has rolled back.
func (s *Service) write(ctx context.Context, a access.Actor, fn func(o *op) error) error {
	var o *op
	err := s.db.InTx(ctx, func(tx *dbx.Tx) error {
		o = &op{s: s, ctx: ctx, tx: tx, actor: a}
		return fn(o)
	})
	if o != nil && o.denied != nil {
		if _, auditErr := s.audit.Record(ctx, *o.denied); auditErr != nil {
			return errors.Join(err, auditErr)
		}
	}
	return err
}

// object is what an audit entry is about: a resource, by its code (stable once published,
// and what the contract identifies resources by).
func resourceObject(code, label string) audit.Object {
	return audit.Object{Kind: "resource", ID: code, Label: label}
}

// check returns ErrDenied unless the actor may do action to t. A refused write is remembered
// for the audit log.
func (o *op) check(action access.Action, t access.Target, obj audit.Object) error {
	d := o.s.access.CanIn(o.ctx, o.tx, o.actor, action, t)
	if d.Err != nil {
		return d.Err
	}
	if d.Allowed {
		return nil
	}
	if action.IsWrite() {
		o.denied = &audit.Entry{Actor: auditActor(o.actor), Action: string(action), Object: obj, Result: audit.ResultDenied, Reason: d.Reason}
	}
	return ErrDenied
}

// record writes the audit entry of the change in the operation's transaction.
func (o *op) record(action access.Action, obj audit.Object, change map[string]any) error {
	if change == nil {
		change = map[string]any{}
	}
	_, err := o.s.audit.Append(o.ctx, o.tx, audit.Entry{Actor: auditActor(o.actor), Action: string(action), Object: obj, Change: change})
	return err
}

// emit publishes p once the transaction has committed.
func (o *op) emit(p events.Payload) {
	if o.s.events != nil {
		o.s.events.Publish(o.tx, p)
	}
}

// can answers a read-only question outside a transaction: ErrDenied, or a database error.
func (s *Service) can(ctx context.Context, a access.Actor, action access.Action, t access.Target) error {
	d := s.access.Can(ctx, a, action, t)
	if d.Err != nil {
		return d.Err
	}
	if !d.Allowed {
		return ErrDenied
	}
	return nil
}

// target describes a resource for a permission decision.
func (r Resource) target() access.Target {
	switch r.Scope {
	case "", "org":
		return access.TargetResource(access.InOrg, "", "", r.Maintainers)
	case "me":
		return access.TargetResource(access.InPersonal, "", r.OwnerID, r.Maintainers)
	default:
		return access.TargetResource(access.InProduct, r.ProductID, "", r.Maintainers)
	}
}

// ref is the part of every event about r.
func (r Resource) ref() events.ResourceRef {
	return events.ResourceRef{ResourceID: r.ID, Code: r.Code, Kind: r.Kind, Name: r.Name, Scope: string(r.Scope),
		ProductID: r.ProductID, Stages: r.Stages, Maintainers: r.Maintainers}
}
