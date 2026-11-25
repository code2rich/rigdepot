// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package accessapi implements the operations of the HTTP contract that
// belong to package access: products and their members, join requests,
// accounts, invitations and the signed-in person's profile. Handlers has one
// method per operation, with the signatures of httpapi.StrictServerInterface,
// to be put together with the other domains' handlers into the whole
// interface.
//
// Like the rest of the HTTP layer it holds no rules: it turns a request into
// a call of access.Service with the actor from the request's context
// (access.ActorFrom, put there by the authentication middleware), and the
// result or the error into a response.
package accessapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

// SignIn is how someone signs in, as package authn knows it.
type SignIn struct {
	Methods     []string   // the sign-in methods the account has used: "local", or an identity provider's id
	LastLoginAt *time.Time // nil when it never signed in
}

// Options are what Handlers needs besides the access service. Every field
// may be left empty; what it provides is then left out of the responses.
type Options struct {
	// BaseURL is the server's public address; invitation links are
	// BaseURL + /invite/<token>.
	BaseURL *url.URL
	// ResourceCounts returns, by product id, how many resources are scoped
	// to each product (package catalog). Nil counts none.
	ResourceCounts func(ctx context.Context) (map[string]int, error)
	// SignIns returns, by user id, how the accounts sign in (package authn).
	// Nil leaves identities empty and last_login_at out.
	SignIns func(ctx context.Context, userIDs []string) (map[string]SignIn, error)
	// MailInvite sends an invitation link (package notify). Nil, or an
	// error, shows the link to the administrator in the response instead.
	MailInvite func(ctx context.Context, inv access.Invite, link string) error
}

// Handlers answers the operations of package access.
type Handlers struct {
	svc *access.Service
	o   Options
}

// New returns the handlers on svc.
func New(svc *access.Service, o Options) *Handlers {
	return &Handlers{svc: svc, o: o}
}

// errNoActor means the request is not signed in.
var errNoActor = errors.New("accessapi: not signed in")

// errNoBody means a required request body is missing.
var errNoBody = &access.Error{Kind: access.KindInvalid, Code: httpapi.CodeRequestInvalid, Fields: []access.FieldError{{Field: "body", Code: "required"}}}

func actor(ctx context.Context) (access.Actor, error) {
	a, ok := access.ActorFrom(ctx)
	if !ok {
		return access.Actor{}, errNoActor
	}
	return a, nil
}

// details are the English texts of the problems this package produces.
// TODO(i18n): render them in the request's language once locales/server.json
// exists (docs/architecture/09-i18n.md).
var details = map[string]string{
	"auth.required":               "Sign in first.",
	"access.denied":               "You are not allowed to do this.",
	"object.not_found":            "Not found, or not visible to you.",
	"request.invalid":             "The request has invalid fields.",
	"user.last_admin":             "The platform needs at least one administrator.",
	"product.last_owner":          "A product needs at least one owner.",
	"product.code_taken":          "This product code is already taken.",
	"product.code_reserved":       "This word is a command name or a scope and cannot be a product code.",
	"user.disabled":               "This account is disabled.",
	"user.username_taken":         "This username is already taken.",
	"join_request.already_member": "You are already in this product.",
	"join_request.decided":        "This request has already been decided.",
	"invite.account_exists":       "There is already an account with this e-mail address.",
	"invite.expired":              "This invitation has expired or was already used. Ask for a new one.",
}

var statusOf = map[access.Kind]int{
	access.KindInvalid:  http.StatusBadRequest,
	access.KindDenied:   http.StatusForbidden,
	access.KindNotFound: http.StatusNotFound,
	access.KindConflict: http.StatusConflict,
	access.KindGone:     http.StatusGone,
}

// problem turns an error into a problem document. It returns false for
// errors that are faults rather than answers; those become 500.
func problem(ctx context.Context, err error) (httpapi.Problem, bool) {
	var p httpapi.Problem
	var e *access.Error
	switch {
	case errors.Is(err, errNoActor):
		p = httpapi.Problem{Status: http.StatusUnauthorized, Code: "auth.required"}
	case errors.As(err, &e):
		status, ok := statusOf[e.Kind]
		if !ok {
			return p, false
		}
		p = httpapi.Problem{Status: status, Code: e.Code}
		if len(e.Params) > 0 {
			params := e.Params
			p.Params = &params
		}
		if len(e.Fields) > 0 {
			fields := make([]httpapi.FieldError, len(e.Fields))
			for i, f := range e.Fields {
				fields[i] = httpapi.FieldError{Field: f.Field, Code: f.Code}
			}
			p.Errors = &fields
		}
	default:
		return p, false
	}
	p.Type, p.Title = "about:blank", http.StatusText(p.Status)
	if d, ok := details[p.Code]; ok {
		p.Detail = &d
	}
	if id := logging.RequestID(ctx); id != "" {
		p.RequestId = &id
	}
	return p, true
}

// fail answers err with the operation's problem response made by wrap, or
// passes it on as a fault.
func fail[R any](ctx context.Context, err error, wrap func(httpapi.Problem) R) (R, error) {
	p, ok := problem(ctx, err)
	if !ok {
		var zero R
		return zero, err
	}
	return wrap(p), nil
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func userRef(u access.UserRef) httpapi.UserRef {
	return httpapi.UserRef{Id: u.ID, Username: u.Username, DisplayName: u.DisplayName}
}

func productRef(p access.ProductRef) httpapi.ProductRef {
	return httpapi.ProductRef{Code: p.Code, Name: p.Name}
}

func memberships(ms []access.Membership) []httpapi.Membership {
	out := make([]httpapi.Membership, len(ms))
	for i, m := range ms {
		out[i] = httpapi.Membership{Product: productRef(m.Product), Role: httpapi.Role(m.Role)}
	}
	return out
}
