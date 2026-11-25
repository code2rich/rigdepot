// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import (
	"errors"
	"fmt"
)

// Kind is what sort of failure an Error is. The HTTP layer maps each kind to a status; the
// domain does not know about HTTP.
type Kind int

// The kinds of failure.
const (
	KindInvalid              Kind = iota + 1 // the input is not valid; Fields says which
	KindDenied                               // the actor may not do this
	KindNotFound                             // it does not exist, or the actor may not know it does
	KindConflict                             // the state does not allow it (a must-fix finding, no draft…)
	KindPreconditionFailed                   // If-Match names a state that is gone
	KindPreconditionRequired                 // If-Match is needed and missing
	KindTooLarge                             // an upload is over the limit
	KindUnsupported                          // an upload is not an archive this package reads
)

// Error is a failure the caller can act on. Code is the stable identifier of the API's problem
// documents (`<area>.<reason>`).
type Error struct {
	Kind   Kind
	Code   string
	Params map[string]any
	Fields []FieldError // for KindInvalid
}

// FieldError is one invalid field of the input.
type FieldError struct {
	Field  string // where in the request: "meta.scope", "to_version", a file of an upload
	Code   string // required, too_long, enum, unknown, or a reader's issue code
	Detail string // optional, in English
}

func (e *Error) Error() string {
	if len(e.Fields) > 0 {
		return fmt.Sprintf("catalog: %s %v", e.Code, e.Fields)
	}
	return "catalog: " + e.Code
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
	ErrDenied               = &Error{Kind: KindDenied, Code: "access.denied"}
	ErrNotFound             = &Error{Kind: KindNotFound, Code: "object.not_found"}
	ErrInvalid              = &Error{Kind: KindInvalid, Code: "request.invalid"}
	ErrPreconditionFailed   = &Error{Kind: KindPreconditionFailed, Code: "precondition.failed"}
	ErrPreconditionRequired = &Error{Kind: KindPreconditionRequired, Code: "precondition.required"}
	ErrCheckBlocked         = &Error{Kind: KindConflict, Code: "resource.check_blocked"}
	ErrNoDraft              = &Error{Kind: KindConflict, Code: "resource.no_draft"}
	ErrFromRepository       = &Error{Kind: KindConflict, Code: "resource.from_repository"}
	ErrKindMismatch         = &Error{Kind: KindConflict, Code: "resource.kind_mismatch"}
	ErrNotWaivable          = &Error{Kind: KindConflict, Code: "check.not_waivable"}
	ErrPlaintextSecret      = &Error{Kind: KindInvalid, Code: "resource.plaintext_secret"}
	ErrUnsafePath           = &Error{Kind: KindInvalid, Code: "bundle.unsafe_path"}
	ErrUnknownFormat        = &Error{Kind: KindUnsupported, Code: "bundle.unknown_format"}
	ErrTooLarge             = &Error{Kind: KindTooLarge, Code: "request.too_large"}
	// ErrCodeTaken: an import found a resource with the same code that comes from elsewhere
	// (another repository, an upload, the web).
	ErrCodeTaken = &Error{Kind: KindConflict, Code: "resource.code_taken"}
)

// invalid builds a KindInvalid error naming the fields.
func invalid(fields ...FieldError) error {
	return &Error{Kind: KindInvalid, Code: ErrInvalid.Code, Fields: fields}
}
