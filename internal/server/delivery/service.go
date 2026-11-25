// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/catalog"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Kind is what sort of failure an Error is. The HTTP layer maps each kind to a status.
type Kind int

// The kinds of failure.
const (
	KindInvalid  Kind = iota + 1 // the input is not valid; Fields says which
	KindDenied                   // the actor may not do this
	KindNotFound                 // it does not exist, or the actor may not know it does
	KindConflict                 // the state does not allow it
)

// Error is a failure the caller can act on. Code is the stable identifier of the API's problem
// documents.
type Error struct {
	Kind   Kind
	Code   string
	Params map[string]any
	Fields []FieldError // for KindInvalid
}

// FieldError is one invalid field of the input.
type FieldError struct {
	Field string // where in the request, such as "installs[3].reason"
	Code  string // required, enum, pattern, too_long, unknown
}

func (e *Error) Error() string {
	if len(e.Fields) > 0 {
		return fmt.Sprintf("delivery: %s %v", e.Code, e.Fields)
	}
	return "delivery: " + e.Code
}

// Is matches errors of the same code, whatever the parameters.
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == e.Code
}

func (e *Error) with(params map[string]any) *Error {
	c := *e
	c.Params = params
	return &c
}

// The failures of this package.
var (
	ErrDenied   = &Error{Kind: KindDenied, Code: "access.denied"}
	ErrNotFound = &Error{Kind: KindNotFound, Code: "object.not_found"}
	ErrInvalid  = &Error{Kind: KindInvalid, Code: "request.invalid"}
	// ErrRequired: turning off a resource whose policy is required.
	ErrRequired = &Error{Kind: KindConflict, Code: "resource.required"}
)

func invalid(fields ...FieldError) error {
	return &Error{Kind: KindInvalid, Code: ErrInvalid.Code, Fields: fields}
}

// Options are what the service needs besides the database, access and the catalog.
type Options struct {
	// Auditor records the preference changes and their refusals. Required.
	Auditor *audit.Log
	// LiveDevices returns the computers that are signed in right now, as device id → user id
	// (package authn keeps the devices). Nil counts every computer that ever reported.
	LiveDevices func(ctx context.Context) (map[string]string, error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Service is the delivery domain.
type Service struct {
	db      *dbx.DB
	access  *access.Service
	catalog *catalog.Service
	audit   *audit.Log
	live    func(ctx context.Context) (map[string]string, error)
	now     func() time.Time
}

// New returns the service on db.
func New(db *dbx.DB, acc *access.Service, cat *catalog.Service, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{db: db, access: acc, catalog: cat, audit: o.Auditor, live: o.LiveDevices, now: o.Now}
}

func (s *Service) millis() int64 { return dbx.Millis(s.now()) }

// Now is the service's clock.
func (s *Service) Now() time.Time { return s.now() }

// auditActor is an access.Actor as the audit log records it.
func auditActor(a access.Actor) audit.Actor {
	return audit.Actor{UserID: a.UserID, Origin: audit.Origin(a.Origin), ViaTool: a.ViaTool, DeviceID: a.DeviceID}
}
