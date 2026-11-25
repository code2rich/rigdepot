// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Kind is what sort of failure an Error is. The HTTP layer maps each kind to
// a status; this package does not know about HTTP.
type Kind int

// The kinds of failure.
const (
	KindInvalid         Kind = iota + 1 // the input is not valid; Fields says which
	KindUnauthenticated                 // no valid credential
	KindDenied                          // signed in, but not allowed
	KindNotFound                        // not there, or not the caller's
	KindConflict                        // the state does not allow it
	KindLocked                          // too many wrong passwords; RetryAfter says how long
)

// Error is a failure the caller can act on. Code is the stable identifier of
// the API's problem documents.
type Error struct {
	Kind       Kind
	Code       string
	Fields     []access.FieldError // for KindInvalid
	RetryAfter time.Duration       // for KindLocked
}

func (e *Error) Error() string {
	if len(e.Fields) > 0 {
		return fmt.Sprintf("authn: %s %v", e.Code, e.Fields)
	}
	return "authn: " + e.Code
}

// Is matches errors of the same code, so that errors.Is(err, ErrLocked)
// works whatever the details.
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == e.Code
}

// The failures of this package.
var (
	// ErrUnauthenticated: no credential, or one that is unknown, expired,
	// revoked, or belongs to a disabled account.
	ErrUnauthenticated = &Error{Kind: KindUnauthenticated, Code: "auth.required"}
	// ErrInvalidCredentials: the account name or the password is wrong. The
	// same answer whether the account exists or not.
	ErrInvalidCredentials = &Error{Kind: KindUnauthenticated, Code: "auth.invalid_credentials"}
	// ErrLocked: too many wrong passwords in a row.
	ErrLocked = &Error{Kind: KindLocked, Code: "auth.locked"}
	// ErrDisabled: the password is right but the account is disabled.
	ErrDisabled = &Error{Kind: KindDenied, Code: "user.disabled"}
	// ErrDenied: the caller may not do this (for example, issue an access
	// token from the command line).
	ErrDenied = &Error{Kind: KindDenied, Code: "access.denied"}
	// ErrNotFound: not there, or not the caller's.
	ErrNotFound = &Error{Kind: KindNotFound, Code: "object.not_found"}
	// ErrNoPassword: the account signs in through an identity provider and
	// has no local password.
	ErrNoPassword = &Error{Kind: KindConflict, Code: "auth.no_password"}
)

func invalid(fields ...access.FieldError) error {
	return &Error{Kind: KindInvalid, Code: "request.invalid", Fields: fields}
}

func locked(retry time.Duration) error {
	e := *ErrLocked
	e.RetryAfter = retry.Round(time.Second)
	if e.RetryAfter < time.Second {
		e.RetryAfter = time.Second
	}
	return &e
}

// DeviceTokens verifies the device tokens of the command-line program. The
// device code flow provides it (task M1-22); without it every device token
// is refused.
type DeviceTokens interface {
	// VerifyDeviceToken returns the account and the computer a device token
	// belongs to, or ErrUnauthenticated when it is not a live one. The
	// account's status is checked by the caller.
	VerifyDeviceToken(ctx context.Context, token string) (userID, deviceID string, err error)
	// SignOutDevices signs out every computer of the account inside tx
	// (signing out everywhere) and returns how many there were.
	SignOutDevices(ctx context.Context, tx *dbx.Tx, userID string) (int, error)
}

// Options are what Service needs besides the database.
type Options struct {
	// Access keeps the accounts. Required.
	Access *access.Service
	// Audit receives every sign-in, sign-out and change of a credential.
	// Required.
	Audit *audit.Log
	// Devices verifies device tokens; nil refuses them all.
	Devices DeviceTokens
	// Hash sets the cost of password hashes. The zero value is
	// DefaultHashParams; tests make it cheaper.
	Hash HashParams
	// MaxConcurrentHashes is how many password hashes may run at once (each
	// takes Hash.Memory). Zero is 4.
	MaxConcurrentHashes int
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Service signs people in and keeps their sessions and access tokens.
type Service struct {
	db      *dbx.DB
	access  *access.Service
	audit   *audit.Log
	devices DeviceTokens
	hasher  *hasher
	now     func() time.Time
}

// New returns the service on db.
func New(db *dbx.DB, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Hash == (HashParams{}) {
		o.Hash = DefaultHashParams
	}
	if o.MaxConcurrentHashes <= 0 {
		o.MaxConcurrentHashes = 4
	}
	return &Service{
		db: db, access: o.Access, audit: o.Audit, devices: o.Devices,
		hasher: newHasher(o.Hash, o.MaxConcurrentHashes), now: o.Now,
	}
}

func (s *Service) millis() int64 { return dbx.Millis(s.now()) }

// Caller is who sent a request, as its credential says.
type Caller struct {
	// Actor is what the domain services receive. Origin is web for a
	// session and cli for device and access tokens; TokenID and Scope are
	// set for access tokens, DeviceID for device tokens.
	Actor access.Actor
	// SessionID is the session of a browser; "" for tokens.
	SessionID string
}

// browser reports whether c is a browser session: the operations that take
// passwords or hand out tokens only work there.
func (c Caller) browser() bool {
	return c.SessionID != "" && c.Actor.Origin == access.OriginWeb && c.Actor.TokenID == "" && c.Actor.DeviceID == ""
}

// liveUser returns the account behind a credential, or ErrUnauthenticated
// when it no longer exists or is disabled. It is read on every request, so
// disabling an account takes effect with its next one.
func (s *Service) liveUser(ctx context.Context, userID string) (access.User, error) {
	u, err := s.access.GetUser(ctx, access.System, userID)
	switch {
	case errors.Is(err, access.ErrNotFound):
		return u, ErrUnauthenticated
	case err != nil:
		return u, err
	case u.Status == access.StatusActive, u.Status == access.StatusPending:
		return u, nil
	}
	// Disabled, or a status this version does not know.
	return u, ErrUnauthenticated
}

// The audit log's actions of this package. They are about one's own
// credentials, which access has no rule for: the person they belong to may
// do them, nobody else.
const (
	ActionLogin            = "auth.login"
	ActionLogout           = "auth.logout"
	ActionSignOutAll       = "auth.sign_out_all"
	ActionPasswordChange   = "auth.password.change"
	ActionAccessTokenIssue = "access_token.create"
	ActionAccessTokenDrop  = "access_token.revoke"
)

// MethodLocal is the sign-in method of local accounts in the audit log.
const MethodLocal = "local"

func userObject(id, username string) audit.Object {
	return audit.Object{Kind: "user", ID: id, Label: username}
}

func webActor(userID string) audit.Actor {
	return audit.Actor{UserID: userID, Origin: audit.OriginWeb}
}

// auditActor is how the audit log records a caller.
func auditActor(a access.Actor) audit.Actor {
	return audit.Actor{UserID: a.UserID, Origin: audit.Origin(a.Origin), ViaTool: a.ViaTool, DeviceID: a.DeviceID}
}
