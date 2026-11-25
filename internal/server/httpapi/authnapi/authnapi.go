// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package authnapi is the HTTP side of signing in (package authn): the
// middleware that turns the credential of every request into the caller
// (Ops.Middleware), and the operations of the contract about signing in and
// out, one's own password, access tokens and accepting invitations. Ops has
// one method per operation, with the signatures of
// httpapi.StrictServerInterface, to be put together with the other domains'
// handlers into the whole interface. It also creates the setup wizard's
// first administrator for the server (CreateFirstAdmin, SignIn).
//
// Like the rest of the HTTP layer it holds no rules: what a session or a
// token is worth, locks, and the audit log are package authn's.
package authnapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/httpapi/accessapi"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// SessionCookie is the name of the session cookie (the contract's session
// security scheme).
const SessionCookie = brand.CLI + "_session"

// Options are what Ops needs besides the sign-in service.
type Options struct {
	// BaseURL is the server's public address. Its scheme decides whether
	// the session cookie is Secure, its path the cookie's path, its origin
	// the origin cross-site protection trusts. Required.
	BaseURL *url.URL
	// TrustedProxies are the reverse proxies whose X-Forwarded-For is
	// believed, for limits by client address.
	TrustedProxies []netip.Prefix
	// SignInRate limits signing in (with a password, and through identity
	// providers, separately), opening invitation links and starting rgd
	// login per client address; UserRate limits every other operation per account. The zero
	// value is DefaultSignInRate and DefaultUserRate; a negative Burst
	// turns a limit off.
	SignInRate, UserRate Rate
	// Now is the limits' clock; nil is time.Now.
	Now func() time.Time
	// Log receives faults while authenticating; nil discards them.
	Log *slog.Logger
	// Devices runs the device code flow and keeps the devices (rgd login,
	// the confirmation page, my devices). Nil: those operations answer 501.
	Devices *authn.Devices
	// Protected are path prefixes outside the contract (the optional
	// modules' routes, /api/v1/x/) whose requests are authenticated like an
	// operation that accepts a session, a device token or an access token
	// with at least the read scope. Other paths outside the contract pass
	// through without a caller.
	Protected []string
}

// Ops answers the operations of package authn and authenticates every
// request (Middleware).
type Ops struct {
	protected []string
	svc       *authn.Service
	devices   *authn.Devices
	base      string // the public address without a trailing slash, for verification_uri and redirects
	log       *slog.Logger
	ops       *operations
	cop       *http.CrossOriginProtection
	secure    bool
	path      string

	trusted    []netip.Prefix
	signIn     *limiter
	invite     *limiter
	deviceCode *limiter
	provider   *limiter
	user       *limiter
}

// New returns the operations on svc. It fails when the contract built into
// the program names a security scheme this package does not know.
func New(svc *authn.Service, o Options) (*Ops, error) {
	ops, err := contract()
	if err != nil {
		return nil, err
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = logging.Discard()
	}
	if o.SignInRate == (Rate{}) {
		o.SignInRate = DefaultSignInRate
	}
	if o.UserRate == (Rate{}) {
		o.UserRate = DefaultUserRate
	}
	h := &Ops{
		protected: o.Protected, devices: o.Devices,
		svc: svc, log: o.Log, ops: ops, cop: http.NewCrossOriginProtection(), path: "/", trusted: o.TrustedProxies,
		signIn: newLimiter(o.SignInRate, o.Now), invite: newLimiter(o.SignInRate, o.Now), deviceCode: newLimiter(o.SignInRate, o.Now), provider: newLimiter(o.SignInRate, o.Now), user: newLimiter(o.UserRate, o.Now),
	}
	if b := o.BaseURL; b != nil {
		h.secure = b.Scheme == "https"
		h.base = strings.TrimSuffix(b.String(), "/")
		if b.Path != "" && b.Path != "/" {
			h.path = b.Path
		}
		// Behind a proxy that rewrites Host, the origin the browser sends is
		// still the public one.
		if err := h.cop.AddTrustedOrigin(b.Scheme + "://" + b.Host); err != nil {
			return nil, err
		}
	}
	return h, nil
}

// sessionCookie is the Set-Cookie value for a new session. It lasts as long
// as the session can (authn.SessionMax); the server ends it earlier when
// it is idle.
func (h *Ops) sessionCookie(in authn.SignedIn) string {
	c := http.Cookie{
		Name: SessionCookie, Value: in.Token, Path: h.path, HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(authn.SessionMax / time.Second),
	}
	return c.String()
}

// clearCookie is the Set-Cookie value that removes the session cookie.
func (h *Ops) clearCookie() string {
	c := http.Cookie{Name: SessionCookie, Value: "", Path: h.path, HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1}
	return c.String()
}

// details are the English texts of the problems this package produces.
// TODO(i18n): render them in the request's language once locales/server.json
// exists (docs/architecture/09-i18n.md).
var details = map[string]string{
	"auth.required":            "Sign in first.",
	"auth.invalid_credentials": "The account name or the password is wrong.",
	"auth.locked":              "Too many wrong passwords. Try again later.",
	"auth.no_password":         "This account signs in through an identity provider and has no password here.",
	"user.disabled":            "This account is disabled.",
	"access.denied":            "You are not allowed to do this.",
	"object.not_found":         "Not found, or not visible to you.",
	"request.invalid":          "The request has invalid fields.",
	"precondition.failed":      "This changed after you opened it. Reload to see the latest.",
	"precondition.required":    "Send If-Match with the ETag you read.",
	"identity.last_method":     "At least one sign-in method has to stay enabled.",
	"identity.local_fixed":     "Local accounts can only be turned on and off.",
	"auth.method_disabled":     "Signing in with a password is turned off on this server.",
	"rate.limited":             "Too many requests. Try again in a moment.",

	"device.authorization_pending": "Waiting for confirmation in the browser.",
	"device.slow_down":             "Polling too fast; wait longer between requests.",
	"device.expired":               "This sign-in request has expired or was already used. Start again.",
	"device.denied":                "The sign-in request was refused in the browser.",
	"device.decided":               "This sign-in request was already approved or refused.",
}

var statusOf = map[authn.Kind]int{
	authn.KindInvalid:              http.StatusBadRequest,
	authn.KindUnauthenticated:      http.StatusUnauthorized,
	authn.KindDenied:               http.StatusForbidden,
	authn.KindNotFound:             http.StatusNotFound,
	authn.KindConflict:             http.StatusConflict,
	authn.KindLocked:               http.StatusTooManyRequests,
	authn.KindDeviceFlow:           http.StatusBadRequest, // RFC 8628, 3.5
	authn.KindPrecondition:         http.StatusPreconditionFailed,
	authn.KindPreconditionRequired: http.StatusPreconditionRequired,
}

// newProblem is a problem document with this package's detail text and the
// request id.
func newProblem(ctx context.Context, status int, code string) httpapi.Problem {
	p := httpapi.Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Code: code}
	if d, ok := details[code]; ok {
		p.Detail = &d
	}
	if id := logging.RequestID(ctx); id != "" {
		p.RequestId = &id
	}
	return p
}

// fail turns an error of authn or access into its problem document, as an
// error the generated handler answers (httpapi.ProblemError). Other errors
// stay faults (500). A lock also sets Retry-After.
func fail(ctx context.Context, err error) error {
	var e *authn.Error
	if errors.As(err, &e) {
		status, ok := statusOf[e.Kind]
		if !ok {
			return err
		}
		p := newProblem(ctx, status, e.Code)
		if len(e.Fields) > 0 {
			fields := make([]httpapi.FieldError, len(e.Fields))
			for i, f := range e.Fields {
				fields[i] = httpapi.FieldError{Field: f.Field, Code: f.Code}
			}
			p.Errors = &fields
		}
		if len(e.Params) > 0 {
			params := make(map[string]any, len(e.Params))
			for k, v := range e.Params {
				params[k] = v
			}
			p.Params = &params
		}
		if e.RetryAfter > 0 {
			setRetryAfter(ctx, e.RetryAfter)
		}
		return &httpapi.ProblemError{Problem: p}
	}
	if p, ok := accessapi.ProblemOf(ctx, err); ok {
		return &httpapi.ProblemError{Problem: p}
	}
	return err
}

func errNoBody(ctx context.Context) error {
	p := newProblem(ctx, http.StatusBadRequest, httpapi.CodeRequestInvalid)
	p.Errors = &[]httpapi.FieldError{{Field: "body", Code: "required"}}
	return &httpapi.ProblemError{Problem: p}
}

// caller returns who sends the request, as the middleware found it. A
// request whose actor was put into the context some other way (tests of
// other packages) has no session.
func caller(ctx context.Context) (authn.Caller, error) {
	if c, ok := ctx.Value(callerKey{}).(authn.Caller); ok {
		return c, nil
	}
	if a, ok := access.ActorFrom(ctx); ok {
		return authn.Caller{Actor: a}, nil
	}
	return authn.Caller{}, fail(ctx, authn.ErrUnauthenticated)
}

// Login implements httpapi.StrictServerInterface.
func (h *Ops) Login(ctx context.Context, req httpapi.LoginRequestObject) (httpapi.LoginResponseObject, error) {
	if req.Body == nil {
		return nil, errNoBody(ctx)
	}
	in, err := h.svc.Login(ctx, req.Body.Username, req.Body.Password)
	if err != nil {
		return nil, fail(ctx, err)
	}
	logging.SetUser(ctx, in.User.ID)
	cookie := h.sessionCookie(in)
	return httpapi.Login200JSONResponse{Body: accessapi.MeOf(in.User), Headers: httpapi.Login200ResponseHeaders{SetCookie: &cookie}}, nil
}

// Logout implements httpapi.StrictServerInterface.
func (h *Ops) Logout(ctx context.Context, _ httpapi.LogoutRequestObject) (httpapi.LogoutResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.Logout(ctx, c); err != nil {
		return nil, fail(ctx, err)
	}
	clear := h.clearCookie()
	return httpapi.Logout204Response{Headers: httpapi.Logout204ResponseHeaders{SetCookie: &clear}}, nil
}

// AcceptInvite implements httpapi.StrictServerInterface: the account, its
// password and its session in one go.
func (h *Ops) AcceptInvite(ctx context.Context, req httpapi.AcceptInviteRequestObject) (httpapi.AcceptInviteResponseObject, error) {
	if req.Body == nil {
		return nil, errNoBody(ctx)
	}
	in, err := h.svc.AcceptInvite(ctx, req.InviteToken, access.NewAccount{Username: req.Body.Username, DisplayName: req.Body.DisplayName}, req.Body.Password)
	if err != nil {
		return nil, fail(ctx, err)
	}
	logging.SetUser(ctx, in.User.ID)
	cookie := h.sessionCookie(in)
	return httpapi.AcceptInvite201JSONResponse{Body: accessapi.MeOf(in.User), Headers: httpapi.AcceptInvite201ResponseHeaders{SetCookie: &cookie}}, nil
}

// SignOutEverywhere implements httpapi.StrictServerInterface.
func (h *Ops) SignOutEverywhere(ctx context.Context, _ httpapi.SignOutEverywhereRequestObject) (httpapi.SignOutEverywhereResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.SignOutEverywhere(ctx, c); err != nil {
		return nil, fail(ctx, err)
	}
	var resp httpapi.SignOutEverywhere204Response
	if c.SessionID != "" {
		clear := h.clearCookie()
		resp.Headers.SetCookie = &clear
	}
	return resp, nil
}

// ChangeMyPassword implements httpapi.StrictServerInterface.
func (h *Ops) ChangeMyPassword(ctx context.Context, req httpapi.ChangeMyPasswordRequestObject) (httpapi.ChangeMyPasswordResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errNoBody(ctx)
	}
	if err := h.svc.ChangePassword(ctx, c, req.Body.CurrentPassword, req.Body.NewPassword); err != nil {
		return nil, fail(ctx, err)
	}
	return httpapi.ChangeMyPassword204Response{}, nil
}

func accessToken(t authn.AccessToken) httpapi.AccessToken {
	return httpapi.AccessToken{
		Id: t.ID, Name: t.Name, Prefix: t.Prefix, Scope: httpapi.TokenScope(t.Scope),
		CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt,
	}
}

// ListMyAccessTokens implements httpapi.StrictServerInterface.
func (h *Ops) ListMyAccessTokens(ctx context.Context, _ httpapi.ListMyAccessTokensRequestObject) (httpapi.ListMyAccessTokensResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ts, err := h.svc.AccessTokens(ctx, c)
	if err != nil {
		return nil, fail(ctx, err)
	}
	out := httpapi.ListMyAccessTokens200JSONResponse{Items: make([]httpapi.AccessToken, len(ts))}
	for i, t := range ts {
		out.Items[i] = accessToken(t)
	}
	return out, nil
}

// CreateAccessToken implements httpapi.StrictServerInterface. The plain
// token is in this response and nowhere else.
func (h *Ops) CreateAccessToken(ctx context.Context, req httpapi.CreateAccessTokenRequestObject) (httpapi.CreateAccessTokenResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errNoBody(ctx)
	}
	plain, t, err := h.svc.CreateAccessToken(ctx, c, authn.NewAccessToken{
		Name: req.Body.Name, Scope: access.TokenScope(req.Body.Scope), ExpiresInDays: req.Body.ExpiresInDays,
	})
	if err != nil {
		return nil, fail(ctx, err)
	}
	return httpapi.CreateAccessToken201JSONResponse{Token: plain, AccessToken: accessToken(t)}, nil
}

// RevokeAccessToken implements httpapi.StrictServerInterface.
func (h *Ops) RevokeAccessToken(ctx context.Context, req httpapi.RevokeAccessTokenRequestObject) (httpapi.RevokeAccessTokenResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.RevokeAccessToken(ctx, c, req.TokenId); err != nil {
		return nil, fail(ctx, err)
	}
	return httpapi.RevokeAccessToken204Response{}, nil
}

// CreateFirstAdmin makes the setup wizard's first administrator, with the
// password from the request, inside the wizard's transaction (the server's
// firstAdminCreator). Invalid input is answered as a problem document.
func (h *Ops) CreateFirstAdmin(ctx context.Context, tx *dbx.Tx, req httpapi.SetupAdminRequest) (settings.NewAdmin, error) {
	a := authn.FirstAdmin{Username: req.Username, Password: req.Password}
	if req.DisplayName != nil {
		a.DisplayName = *req.DisplayName
	}
	if req.Email != nil {
		a.Email = string(*req.Email)
	}
	if a.DisplayName == "" {
		a.DisplayName = a.Username
	}
	u, err := h.svc.CreateFirstAdmin(ctx, tx, a)
	if err != nil {
		return settings.NewAdmin{}, fail(ctx, err)
	}
	return settings.NewAdmin{UserID: u.ID, Username: u.Username}, nil
}

// SignIn starts a session for the first administrator after the wizard's
// transaction has committed, and returns the Set-Cookie value and the
// account.
func (h *Ops) SignIn(ctx context.Context, userID string) (string, httpapi.Me, error) {
	in, err := h.svc.StartSession(ctx, userID)
	if err != nil {
		return "", httpapi.Me{}, fail(ctx, err)
	}
	logging.SetUser(ctx, userID)
	return h.sessionCookie(in), accessapi.MeOf(in.User), nil
}

// SignIns returns, by user id, how the accounts sign in, as
// accessapi.Options.SignIns wants it.
func SignIns(svc *authn.Service) func(ctx context.Context, userIDs []string) (map[string]accessapi.SignIn, error) {
	return func(ctx context.Context, userIDs []string) (map[string]accessapi.SignIn, error) {
		infos, err := svc.SignIns(ctx, userIDs)
		if err != nil {
			return nil, err
		}
		out := make(map[string]accessapi.SignIn, len(infos))
		for id, i := range infos {
			out[id] = accessapi.SignIn{Methods: i.Methods, LastLoginAt: i.LastLoginAt}
		}
		return out, nil
	}
}

// retryAfter writes the seconds of d, rounded up, for Retry-After.
func retryAfter(d time.Duration) string {
	return strconv.Itoa(int((d + time.Second - 1) / time.Second))
}
