// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package feedback

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/catalog"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/events"
)

// Auditor writes the audit log. *audit.Log implements it.
type Auditor interface {
	Append(ctx context.Context, tx *dbx.Tx, e audit.Entry) (int64, error)
	Record(ctx context.Context, e audit.Entry) (int64, error)
}

// Events receives the feedback events, delivered once the change has committed. *events.Bus
// implements it.
type Events interface {
	Publish(tx events.Tx, p events.Payload)
}

// Resources finds the resources feedback is about. *catalog.Service implements it.
type Resources interface {
	BriefByCode(ctx context.Context, code string) (catalog.Brief, bool, error)
	Briefs(ctx context.Context, ids []string) (map[string]catalog.Brief, error)
}

// Options are what a Service needs besides the database, access and the catalog.
type Options struct {
	Auditor Auditor          // required
	Events  Events           // nil drops events
	Now     func() time.Time // nil is time.Now
}

// Service is the feedback queue.
type Service struct {
	db     *dbx.DB
	access *access.Service
	res    Resources
	audit  Auditor
	events Events
	now    func() time.Time
}

// New returns the feedback queue on db. It fails without an Auditor.
func New(db *dbx.DB, acc *access.Service, res Resources, o Options) (*Service, error) {
	if o.Auditor == nil {
		return nil, errors.New("feedback: New needs an Auditor")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{db: db, access: acc, res: res, audit: o.Auditor, events: o.Events, now: o.Now}, nil
}

func (s *Service) millis() int64 { return dbx.Millis(s.now()) }

func auditActor(a access.Actor) audit.Actor {
	return audit.Actor{UserID: a.UserID, Origin: audit.Origin(a.Origin), ViaTool: a.ViaTool, DeviceID: a.DeviceID}
}

// object is what an audit entry is about: feedback, by its number. The title is the
// submitter's text and stays out of the log.
func object(number int) audit.Object {
	n := strconv.Itoa(number)
	return audit.Object{Kind: "feedback", ID: n, Label: "FB-" + n}
}

// op is one write in progress, as in package catalog.
type op struct {
	s      *Service
	ctx    context.Context
	tx     *dbx.Tx
	actor  access.Actor
	denied *audit.Entry
}

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

// check returns ErrDenied unless the actor may do action to t; a refused write is remembered
// for the audit log.
func (o *op) check(action access.Action, t access.Target, obj audit.Object) error {
	d := o.s.access.CanIn(o.ctx, o.tx, o.actor, action, t)
	if d.Err != nil {
		return d.Err
	}
	if d.Allowed {
		return nil
	}
	o.denied = &audit.Entry{Actor: auditActor(o.actor), Action: string(action), Object: obj, Result: audit.ResultDenied, Reason: d.Reason}
	return ErrDenied
}

func (o *op) record(action access.Action, obj audit.Object, change map[string]any) error {
	_, err := o.s.audit.Append(o.ctx, o.tx, audit.Entry{Actor: auditActor(o.actor), Action: string(action), Object: obj, Change: change})
	return err
}

func (o *op) emit(p events.Payload) {
	if o.s.events != nil {
		o.s.events.Publish(o.tx, p)
	}
}

// handles reports whether a may handle feedback about target (change its status, assign it):
// for a product's resource its owners, for any resource its maintainers, and platform
// administrators (access.ActionFeedbackManage).
func (s *Service) handles(ctx context.Context, q dbx.Querier, a access.Actor, t access.Target) (bool, error) {
	d := s.access.CanIn(ctx, q, a, access.ActionFeedbackManage, t)
	return d.Allowed, d.Err
}

// target is what a decision about fb looks at: its resource, or the platform for feedback
// about no resource (or one that no longer exists), which platform administrators handle.
func (s *Service) target(ctx context.Context, resourceID string) (access.Target, error) {
	if resourceID == "" {
		return access.TargetPlatform(), nil
	}
	bs, err := s.res.Briefs(ctx, []string{resourceID})
	if err != nil {
		return access.Target{}, err
	}
	if b, ok := bs[resourceID]; ok {
		return b.Target, nil
	}
	return access.TargetPlatform(), nil
}

// sees reports whether a may see fb: its submitter, its assignee, and whoever handles it.
func (s *Service) sees(ctx context.Context, q dbx.Querier, a access.Actor, fb Feedback, t access.Target) (bool, error) {
	if a.IsSystem() {
		return true, nil
	}
	if a.UserID != "" && (a.UserID == fb.SubmitterID || a.UserID == fb.AssigneeID) {
		return true, nil
	}
	return s.handles(ctx, q, a, t)
}

// ref is the part of every event about fb.
func (s *Service) ref(fb Feedback) events.FeedbackRef {
	r := events.FeedbackRef{FeedbackID: fb.ID, Number: fb.Number, SubmitterID: fb.SubmitterID, AssigneeID: fb.AssigneeID}
	if fb.Resource != nil {
		r.ResourceCode = fb.Resource.Code
	}
	return r
}
