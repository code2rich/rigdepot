// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access

import (
	"errors"
	"fmt"
)

// Kind is what sort of failure an Error is. The HTTP layer maps each kind
// to a status; the domain does not know about HTTP.
type Kind int

// The kinds of failure.
const (
	KindInvalid  Kind = iota + 1 // the input is not valid; Fields says which
	KindDenied                   // the actor may not do this
	KindNotFound                 // it does not exist, or the actor may not know it does
	KindConflict                 // it would break a rule about the state, such as the last owner
	KindGone                     // it existed but is used up or expired
)

// Error is a failure the caller can act on. Code is the stable identifier of
// the API's problem documents (`<area>.<reason>`).
type Error struct {
	Kind   Kind
	Code   string
	Params map[string]any
	Fields []FieldError // for KindInvalid
}

// FieldError is one invalid field of the input.
type FieldError struct {
	Field string // the field's name in the request, such as "code" or "emails[2]"
	Code  string // required, too_long, pattern, enum, unknown…
}

func (e *Error) Error() string {
	if len(e.Fields) > 0 {
		return fmt.Sprintf("access: %s %v", e.Code, e.Fields)
	}
	return "access: " + e.Code
}

// Is matches errors of the same code, so that errors.Is(err, ErrLastOwner)
// works whatever the parameters.
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
	ErrDenied        = &Error{Kind: KindDenied, Code: "access.denied"}
	ErrNotFound      = &Error{Kind: KindNotFound, Code: "object.not_found"}
	ErrInvalid       = &Error{Kind: KindInvalid, Code: "request.invalid"}
	ErrLastAdmin     = &Error{Kind: KindConflict, Code: "user.last_admin"}
	ErrLastOwner     = &Error{Kind: KindConflict, Code: "product.last_owner"}
	ErrCodeTaken     = &Error{Kind: KindConflict, Code: "product.code_taken"}
	ErrCodeReserved  = &Error{Kind: KindConflict, Code: "product.code_reserved"}
	ErrUserDisabled  = &Error{Kind: KindConflict, Code: "user.disabled"}
	ErrUsernameTaken = &Error{Kind: KindConflict, Code: "user.username_taken"}
	ErrAlreadyMember = &Error{Kind: KindConflict, Code: "join_request.already_member"}
	ErrDecided       = &Error{Kind: KindConflict, Code: "join_request.decided"}
	ErrAccountExists = &Error{Kind: KindConflict, Code: "invite.account_exists"}
	ErrInviteExpired = &Error{Kind: KindGone, Code: "invite.expired"}
)

// invalid builds a KindInvalid error naming the fields.
func invalid(fields ...FieldError) error {
	return &Error{Kind: KindInvalid, Code: ErrInvalid.Code, Fields: fields}
}
