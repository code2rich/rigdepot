// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authnapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

type callerKey struct{}

type headerKey struct{}

// setRetryAfter sets Retry-After on the response of the request ctx
// belongs to; the middleware hands the header map down.
func setRetryAfter(ctx context.Context, d time.Duration) {
	if h, ok := ctx.Value(headerKey{}).(http.Header); ok {
		h.Set("Retry-After", retryAfter(d))
	}
}

// The operations limited by client address rather than by account: the
// ones anyone can call to try passwords or links.
const (
	opLogin       = "login"
	opGetInvite   = "getInvite"
	opAcceptInv   = "acceptInvite"
	opSetupAdmin  = "createSetupAdmin"
	opDeviceCode  = "requestDeviceCode"
	opDevicePoll  = "pollDeviceToken"
	maxSmallBody  = 64 << 10
	headerViaTool = "X-Via-Tool"
)

// smallBodies are the operations whose bodies are a few fields and may be
// sent before signing in; a larger body is refused before it is read.
var smallBodies = map[string]bool{
	opLogin: true, opAcceptInv: true, opSetupAdmin: true, "changeMyPassword": true, "createAccessToken": true,
	opDeviceCode: true, opDevicePoll: true,
}

// noStore are the operations whose answers carry a credential or the code
// that leads to one: no cache may keep them (RFC 6749, 5.1).
var noStore = map[string]bool{opDeviceCode: true, opDevicePoll: true}

// viaTool is the form of X-Via-Tool: a tool adapter's id.
var viaTool = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Middleware authenticates every request for an operation of the contract
// before next sees it, as the operation's security list says:
//
//   - Cross-site requests with unsafe methods are refused for cookies
//     (http.CrossOriginProtection: Sec-Fetch-Site, then Origin), on top of
//     SameSite=Lax; requests with an Authorization header are not browser
//     sessions and are not checked.
//   - Public operations get no caller. Signing in, invitation links and
//     starting rgd login are limited by client address.
//   - Otherwise the credential is a bearer token (device token or access
//     token, told apart by the prefix) or the session cookie. None, or one
//     that is not valid, is 401 auth.required; a kind of credential the
//     operation does not accept, or an access token whose scope is below
//     the operation's x-token-scope, is 403 access.denied.
//   - The caller goes into the context (access.WithActor, and the session
//     for the sign-in operations) and into the request log
//     (logging.SetUser), and is limited per account.
//
// Requests that are not operations of the contract pass through untouched.
func (h *Ops) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pattern, op, ok := h.ops.find(r)
		if !ok {
			prefix, protected := h.protectedPrefix(r.URL.Path)
			if !protected {
				next.ServeHTTP(w, r)
				return
			}
			pattern = r.Method + " " + prefix + "…"
			op = security{id: "protected:" + prefix, session: true, device: true, token: true, scope: access.TokenRead}
		}
		// The request log names the route, also for requests refused here,
		// before the router sees them.
		logging.SetRoute(r.Context(), pattern)
		ctx := context.WithValue(r.Context(), headerKey{}, w.Header())
		r = r.WithContext(ctx)
		if r.Header.Get("Authorization") == "" {
			if err := h.cop.Check(r); err != nil {
				h.refuse(w, r, newProblem(ctx, http.StatusForbidden, "access.denied"), "cross-site request")
				return
			}
		}
		if smallBodies[op.id] {
			r.Body = http.MaxBytesReader(w, r.Body, maxSmallBody)
		}
		var byAddr *limiter
		switch op.id {
		case opLogin, opSetupAdmin:
			byAddr = h.signIn
		case opGetInvite, opAcceptInv:
			byAddr = h.invite
		case opDeviceCode:
			byAddr = h.deviceCode
		}
		if wait, ok := byAddr.allow(clientAddr(r, h.trusted)); !ok {
			h.tooMany(w, r, wait)
			return
		}
		if noStore[op.id] {
			w.Header().Set("Cache-Control", "no-store")
		}
		if op.public {
			next.ServeHTTP(w, r)
			return
		}

		c, err := h.authenticate(r, op)
		if err != nil {
			var p *httpapi.ProblemError
			if !errors.As(err, &p) {
				h.log.ErrorContext(ctx, "authentication failed", "err", err)
				httpapi.WriteProblem(w, newProblem(ctx, http.StatusInternalServerError, httpapi.CodeServerError))
				return
			}
			if p.Problem.Status == http.StatusUnauthorized && op.optional {
				next.ServeHTTP(w, r)
				return
			}
			h.refuse(w, r, p.Problem, "")
			return
		}
		if wait, ok := h.user.allow(c.Actor.UserID); !ok {
			h.tooMany(w, r, wait)
			return
		}
		ctx = access.WithActor(ctx, c.Actor)
		ctx = context.WithValue(ctx, callerKey{}, c)
		logging.SetUser(ctx, c.Actor.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticate finds the caller of a request for op. Its errors are
// *httpapi.ProblemError, or faults.
func (h *Ops) authenticate(r *http.Request, op security) (authn.Caller, error) {
	ctx := r.Context()
	denied := func() error {
		return &httpapi.ProblemError{Problem: newProblem(ctx, http.StatusForbidden, "access.denied")}
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		scheme, token, _ := strings.Cut(auth, " ")
		token = strings.TrimSpace(token)
		if !strings.EqualFold(scheme, "Bearer") || token == "" {
			return authn.Caller{}, fail(ctx, authn.ErrUnauthenticated)
		}
		var c authn.Caller
		var err error
		switch {
		case strings.HasPrefix(token, authn.DeviceTokenPrefix):
			if !op.device {
				return c, denied()
			}
			c, err = h.svc.Device(ctx, token)
		case strings.HasPrefix(token, authn.AccessTokenPrefix):
			if !op.token {
				return c, denied()
			}
			c, err = h.svc.AccessToken(ctx, token)
			if err == nil && !covers(c.Actor.Scope, op.scope) {
				return c, denied()
			}
		default:
			return c, fail(ctx, authn.ErrUnauthenticated)
		}
		if err != nil {
			return c, fail(ctx, err)
		}
		if v := r.Header.Get(headerViaTool); viaTool.MatchString(v) {
			c.Actor.ViaTool = v
		}
		return c, nil
	}
	cookie, err := r.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		return authn.Caller{}, fail(ctx, authn.ErrUnauthenticated)
	}
	if !op.session {
		return authn.Caller{}, denied()
	}
	c, err := h.svc.Session(ctx, cookie.Value)
	if errors.Is(err, authn.ErrUnauthenticated) {
		// The browser keeps a cookie that no longer works: drop it.
		setCookie(ctx, h.clearCookie())
	}
	if err != nil {
		return c, fail(ctx, err)
	}
	return c, nil
}

func setCookie(ctx context.Context, v string) {
	if h, ok := ctx.Value(headerKey{}).(http.Header); ok {
		h.Add("Set-Cookie", v)
	}
}

func (h *Ops) refuse(w http.ResponseWriter, r *http.Request, p httpapi.Problem, why string) {
	if why != "" {
		h.log.InfoContext(r.Context(), "request refused", "why", why)
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteProblem(w, p)
}

func (h *Ops) tooMany(w http.ResponseWriter, r *http.Request, wait time.Duration) {
	w.Header().Set("Retry-After", retryAfter(wait))
	h.refuse(w, r, newProblem(r.Context(), http.StatusTooManyRequests, "rate.limited"), "")
}

// protectedPrefix returns the prefix in Options.Protected that path falls
// under, if any.
func (h *Ops) protectedPrefix(path string) (string, bool) {
	for _, p := range h.protected {
		if strings.HasPrefix(path, p) {
			return p, true
		}
	}
	return "", false
}
