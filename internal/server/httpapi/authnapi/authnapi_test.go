// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authnapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"

	"github.com/code2rich/rigdepot/api"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/httpapi/accessapi"
	"github.com/code2rich/rigdepot/internal/server/httpapi/authnapi"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// nobody stands in for the operations of the other domains, one level
// deeper than the real handlers; any other operation panics, which a test
// would notice.
type nobody struct{ httpapi.StrictServerInterface }

// stubs are two operations that accept access tokens, standing in for
// package catalog: the stages (scope read) and the resources (scope
// publish), which asks access whether the caller may edit organisation
// resources.
type stubs struct {
	access *access.Service
	last   *access.Actor // the caller of the last ListStages
}

func (s stubs) ListStages(ctx context.Context, _ httpapi.ListStagesRequestObject) (httpapi.ListStagesResponseObject, error) {
	*s.last, _ = access.ActorFrom(ctx)
	return httpapi.ListStages200JSONResponse{Items: []httpapi.Stage{}}, nil
}

func (s stubs) ListResources(ctx context.Context, _ httpapi.ListResourcesRequestObject) (httpapi.ListResourcesResponseObject, error) {
	a, _ := access.ActorFrom(ctx)
	if !s.access.Can(ctx, a, access.ActionResourceEdit, access.TargetOrg()).Allowed {
		return nil, httpapi.NewProblem(http.StatusForbidden, "access.denied", "")
	}
	return httpapi.ListResources200JSONResponse{Items: []httpapi.ResourceSummary{}}, nil
}

type server struct {
	*accessapi.Handlers
	*authnapi.Ops
	stubs
	nobody
}

var _ httpapi.StrictServerInterface = server{}

type accessAuditor struct{ log *audit.Log }

func (a accessAuditor) Append(ctx context.Context, tx *dbx.Tx, e access.AuditEntry) error {
	_, err := a.log.Append(ctx, tx, audit.Entry{
		Actor:  audit.Actor{UserID: e.Actor.UserID, Origin: audit.Origin(e.Actor.Origin)},
		Action: string(e.Action), Object: audit.Object{Kind: e.ObjectKind, ID: e.ObjectID, Label: e.ObjectLabel},
		Change: e.Change, Result: audit.Result(e.Result), Reason: e.Reason,
	})
	return err
}

type fixture struct {
	t      *testing.T
	h      http.Handler
	db     *dbx.DB
	access *access.Service
	authn  *authn.Service
	audit  *audit.Log
	admin  access.User
	router routers.Router
	last   *access.Actor
}

const adminPassword = "<placeholder: admin password>"

func newFixture(t *testing.T, o authnapi.Options) *fixture {
	t.Helper()
	ctx := context.Background()
	db := dbxtest.Open(t, dbxtest.SQLiteURL(t))
	if _, err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	log, err := audit.New(db, make([]byte, audit.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	acc := access.New(db, access.Options{Auditor: accessAuditor{log}})
	svc := authn.New(db, authn.Options{Access: acc, Audit: log, Hash: authn.HashParams{Memory: 64, Time: 1, Threads: 1}})
	f := &fixture{t: t, db: db, access: acc, authn: svc, audit: log, router: contract(t), last: &access.Actor{}}
	err = db.InTx(ctx, func(tx *dbx.Tx) error {
		f.admin, err = svc.CreateFirstAdmin(ctx, tx, authn.FirstAdmin{Username: "admin", DisplayName: "Administrator", Password: adminPassword})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.BaseURL == nil {
		o.BaseURL, _ = url.Parse("https://hub.example.com")
	}
	ops, err := authnapi.New(svc, o)
	if err != nil {
		t.Fatal(err)
	}
	impl := server{Handlers: accessapi.New(acc, accessapi.Options{SignIns: authnapi.SignIns(svc)}), Ops: ops, stubs: stubs{acc, f.last}}
	f.h = ops.Middleware(httpapi.NewHandler(impl, http.NewServeMux()))
	return f
}

var (
	contractOnce   sync.Once
	contractRouter routers.Router
	contractErr    error
)

func contract(t *testing.T) routers.Router {
	t.Helper()
	contractOnce.Do(func() {
		doc, err := openapi3.NewLoader().LoadFromData(api.Spec)
		if err != nil {
			contractErr = err
			return
		}
		doc.Servers = nil
		contractRouter, contractErr = legacyrouter.NewRouter(doc)
	})
	if contractErr != nil {
		t.Fatal(contractErr)
	}
	return contractRouter
}

// request is one call: a session cookie, a bearer token, extra headers.
type request struct {
	method, path string
	body         any
	cookie       string // the session cookie's value
	bearer       string
	header       map[string]string
	remote       string
}

// do sends r, checks the status and that the response is what the contract
// says the operation answers, and decodes the body into out.
func (f *fixture) do(r request, want int, out any) *httptest.ResponseRecorder {
	f.t.Helper()
	var raw []byte
	if r.body != nil {
		var err error
		if raw, err = json.Marshal(r.body); err != nil {
			f.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(r.method, r.path, bytes.NewReader(raw))
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.cookie != "" {
		req.AddCookie(&http.Cookie{Name: authnapi.SessionCookie, Value: r.cookie})
	}
	if r.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+r.bearer)
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	if r.remote != "" {
		req.RemoteAddr = r.remote
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != want {
		f.t.Fatalf("%s %s: %d, want %d\n%s", r.method, r.path, rec.Code, want, rec.Body)
	}
	f.conforms(req, raw, rec)
	if out != nil {
		reflect.ValueOf(out).Elem().SetZero()
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			f.t.Fatalf("%s %s: %v\n%s", r.method, r.path, err, rec.Body)
		}
	}
	return rec
}

func (f *fixture) conforms(req *http.Request, body []byte, rec *httptest.ResponseRecorder) {
	f.t.Helper()
	vreq := req.Clone(context.Background())
	vreq.Body = io.NopCloser(bytes.NewReader(body))
	route, params, err := f.router.FindRoute(vreq)
	if err != nil {
		f.t.Fatalf("%s %s is not in the contract: %v", req.Method, req.URL, err)
	}
	opts := &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, IncludeResponseStatus: true}
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: vreq, PathParams: params, Route: route, Options: opts},
		Status:                 rec.Code,
		Header:                 rec.Header(),
		Options:                opts,
	}
	in.SetBodyBytes(rec.Body.Bytes())
	if err := openapi3filter.ValidateResponse(context.Background(), in); err != nil {
		f.t.Errorf("%s %s answered %d outside the contract: %v\n%s", req.Method, req.URL, rec.Code, err, rec.Body)
	}
}

// sessionOf returns the session cookie's value a response sets.
func sessionOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == authnapi.SessionCookie && c.Value != "" {
			return c.Value
		}
	}
	t.Fatalf("no session cookie in %v", rec.Header())
	return ""
}

func (f *fixture) login(name, password string) string {
	f.t.Helper()
	return sessionOf(f.t, f.do(request{method: "POST", path: "/api/v1/auth/login", body: map[string]string{"username": name, "password": password}}, 200, nil))
}

func TestLoginAndLogout(t *testing.T) {
	f := newFixture(t, authnapi.Options{})
	var p httpapi.Problem
	f.do(request{method: "POST", path: "/api/v1/auth/login", body: map[string]string{"username": "admin", "password": "wrong"}}, 401, &p)
	if p.Code != "auth.invalid_credentials" {
		t.Errorf("wrong password: %+v", p)
	}
	f.do(request{method: "POST", path: "/api/v1/auth/login", body: map[string]string{"username": "nobody", "password": "wrong"}}, 401, &p)
	if p.Code != "auth.invalid_credentials" {
		t.Errorf("unknown account: %+v", p)
	}
	var me httpapi.Me
	rec := f.do(request{method: "POST", path: "/api/v1/auth/login", body: map[string]string{"username": "admin", "password": adminPassword}}, 200, &me)
	if me.Username != "admin" || !me.PlatformAdmin {
		t.Errorf("me: %+v", me)
	}
	set := rec.Header().Get("Set-Cookie")
	for _, want := range []string{authnapi.SessionCookie + "=", "Path=/", "HttpOnly", "Secure", "SameSite=Lax", "Max-Age=2592000"} {
		if !strings.Contains(set, want) {
			t.Errorf("Set-Cookie %q lacks %s", set, want)
		}
	}
	session := sessionOf(t, rec)
	f.do(request{method: "GET", path: "/api/v1/me", cookie: session}, 200, &me)
	rec = f.do(request{method: "POST", path: "/api/v1/auth/logout", cookie: session}, 204, nil)
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].MaxAge >= 0 {
		t.Errorf("logout cookie: %v", rec.Header())
	}
	rec = f.do(request{method: "GET", path: "/api/v1/me", cookie: session}, 401, &p)
	if p.Code != "auth.required" || !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("after logout: %+v %v", p, rec.Header())
	}
	f.do(request{method: "GET", path: "/api/v1/me"}, 401, nil)

	// Every attempt is in the audit log.
	page, err := f.audit.Query(context.Background(), audit.Filter{Category: audit.CategoryLogin}, 0, "")
	if err != nil || len(page.Rows) != 4 {
		t.Fatalf("login entries: %+v %v", page.Rows, err)
	}
}

func TestLockAndRateLimit(t *testing.T) {
	f := newFixture(t, authnapi.Options{SignInRate: authnapi.Rate{Burst: 6, Every: time.Hour}})
	wrong := map[string]string{"username": "admin", "password": "wrong"}
	for range 4 {
		f.do(request{method: "POST", path: "/api/v1/auth/login", body: wrong, remote: "192.0.2.1:1000"}, 401, nil)
	}
	var p httpapi.Problem
	rec := f.do(request{method: "POST", path: "/api/v1/auth/login", body: map[string]string{"username": "admin", "password": adminPassword}, remote: "192.0.2.1:1000"}, 429, &p)
	if p.Code != "auth.locked" || rec.Header().Get("Retry-After") != "1" {
		t.Errorf("locked: %+v %v", p, rec.Header())
	}
	// The address has one attempt left, then it is refused before the
	// password is looked at.
	f.do(request{method: "POST", path: "/api/v1/auth/login", body: wrong, remote: "192.0.2.1:1000"}, 429, &p)
	rec = f.do(request{method: "POST", path: "/api/v1/auth/login", body: wrong, remote: "192.0.2.1:2000"}, 429, &p)
	if p.Code != "rate.limited" || rec.Header().Get("Retry-After") == "" {
		t.Errorf("limited: %+v %v", p, rec.Header())
	}
	// Another address is not.
	f.do(request{method: "POST", path: "/api/v1/auth/login", body: wrong, remote: "192.0.2.2:1000"}, 429, &p)
	if p.Code != "auth.locked" {
		t.Errorf("another address: %+v", p)
	}
	// X-Forwarded-For is only believed from a trusted proxy.
	f.do(request{method: "POST", path: "/api/v1/auth/login", body: wrong, remote: "192.0.2.1:3000", header: map[string]string{"X-Forwarded-For": "198.51.100.7"}}, 429, &p)
	if p.Code != "rate.limited" {
		t.Errorf("forged X-Forwarded-For: %+v", p)
	}
}

func TestClientAddressBehindAProxy(t *testing.T) {
	f := newFixture(t, authnapi.Options{SignInRate: authnapi.Rate{Burst: 1, Every: time.Hour}, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	wrong := map[string]string{"username": "nobody", "password": "wrong"}
	via := func(xff string) request {
		return request{method: "POST", path: "/api/v1/auth/login", body: wrong, remote: "10.1.2.3:443", header: map[string]string{"X-Forwarded-For": xff}}
	}
	f.do(via("198.51.100.7"), 401, nil)
	f.do(via("198.51.100.7"), 429, nil)
	f.do(via("198.51.100.7, 10.9.9.9"), 429, nil)    // through two proxies, same client
	f.do(via("203.0.113.1, 198.51.100.8"), 401, nil) // the client's own claim is not believed: the address is .8
	f.do(via("203.0.113.9, 198.51.100.8"), 429, nil)
}

func TestAcceptInviteOverHTTP(t *testing.T) {
	f := newFixture(t, authnapi.Options{})
	ctx := context.Background()
	invs, err := f.access.CreateInvites(ctx, access.Actor{UserID: f.admin.ID, Origin: access.OriginWeb}, []string{"new.colleague@example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/auth/invites/" + invs[0].Token + "/accept"
	var p httpapi.Problem
	f.do(request{method: "POST", path: path, body: map[string]string{"username": "ncolleague", "display_name": "New Colleague", "password": "short"}}, 400, &p)
	if p.Errors == nil || (*p.Errors)[0] != (httpapi.FieldError{Field: "password", Code: "too_short"}) {
		t.Errorf("short password: %+v", p)
	}
	var me httpapi.Me
	rec := f.do(request{method: "POST", path: path, body: map[string]string{"username": "ncolleague", "display_name": "New Colleague", "password": "<placeholder: long enough>"}}, 201, &me)
	if me.Username != "ncolleague" || me.Email == nil || string(*me.Email) != "new.colleague@example.com" {
		t.Errorf("me: %+v", me)
	}
	f.do(request{method: "GET", path: "/api/v1/me", cookie: sessionOf(t, rec)}, 200, nil)
	f.do(request{method: "POST", path: path, body: map[string]string{"username": "other", "display_name": "Other", "password": "<placeholder: long enough>"}}, 410, &p)
	if p.Code != "invite.expired" {
		t.Errorf("used twice: %+v", p)
	}
	f.do(request{method: "POST", path: "/api/v1/auth/invites/" + strings.Repeat("x", 40) + "/accept", body: map[string]string{"username": "other", "display_name": "Other", "password": "<placeholder: long enough>"}}, 404, nil)

	// The users page shows how the account signs in.
	var u httpapi.User
	f.do(request{method: "GET", path: "/api/v1/users/" + me.Id, cookie: f.login("admin", adminPassword)}, 200, &u)
	if len(u.Identities) != 1 || u.Identities[0] != "local" || u.LastLoginAt == nil {
		t.Errorf("user: %+v", u)
	}
}

func TestChangePasswordOverHTTP(t *testing.T) {
	f := newFixture(t, authnapi.Options{})
	session := f.login("admin", adminPassword)
	other := f.login("admin", adminPassword)
	var p httpapi.Problem
	f.do(request{method: "POST", path: "/api/v1/me/password", cookie: session, body: map[string]string{"current_password": "wrong", "new_password": "<placeholder: new password>"}}, 400, &p)
	if p.Errors == nil || (*p.Errors)[0] != (httpapi.FieldError{Field: "current_password", Code: "mismatch"}) {
		t.Errorf("wrong current: %+v", p)
	}
	f.do(request{method: "POST", path: "/api/v1/me/password", cookie: session, body: map[string]string{"current_password": adminPassword, "new_password": "<placeholder: new password>"}}, 204, nil)
	f.do(request{method: "GET", path: "/api/v1/me", cookie: session}, 200, nil)
	f.do(request{method: "GET", path: "/api/v1/me", cookie: other}, 401, nil)
	f.login("admin", "<placeholder: new password>")
}

func TestAccessTokensOverHTTP(t *testing.T) {
	f := newFixture(t, authnapi.Options{})
	session := f.login("admin", adminPassword)
	issue := func(scope string) httpapi.CreatedAccessToken {
		var out httpapi.CreatedAccessToken
		f.do(request{method: "POST", path: "/api/v1/me/tokens", cookie: session, body: map[string]any{"name": "CI " + scope, "scope": scope, "expires_in_days": 30}}, 201, &out)
		if !strings.HasPrefix(out.Token, authn.AccessTokenPrefix) || !strings.HasPrefix(out.Token, out.AccessToken.Prefix) {
			t.Fatalf("issued: %+v", out)
		}
		return out
	}
	read, publish := issue("read"), issue("publish")
	rec := f.do(request{method: "GET", path: "/api/v1/me/tokens", cookie: session}, 200, nil)
	if strings.Contains(rec.Body.String(), read.Token) || strings.Contains(rec.Body.String(), publish.Token) || !strings.Contains(rec.Body.String(), read.AccessToken.Id) {
		t.Errorf("list: %s", rec.Body)
	}

	var p httpapi.Problem
	// An operation for read tokens. The caller is the token's owner, from
	// the command line, through the AI tool the header names.
	f.do(request{method: "GET", path: "/api/v1/stages", bearer: read.Token, header: map[string]string{"X-Via-Tool": "claude-code"}}, 200, nil)
	if a := *f.last; a.UserID != f.admin.ID || a.TokenID != read.AccessToken.Id || a.Scope != access.TokenRead || a.Origin != access.OriginCLI || a.ViaTool != "claude-code" {
		t.Errorf("caller: %+v", a)
	}
	f.do(request{method: "GET", path: "/api/v1/stages", bearer: read.Token, header: map[string]string{"X-Via-Tool": "Not A Tool!"}}, 200, nil)
	if f.last.ViaTool != "" {
		t.Errorf("an invalid X-Via-Tool was kept: %q", f.last.ViaTool)
	}
	// An operation that needs publish: the read token's scope is short.
	f.do(request{method: "GET", path: "/api/v1/resources", bearer: read.Token}, 403, &p)
	f.do(request{method: "GET", path: "/api/v1/resources", bearer: publish.Token}, 200, nil)
	// Operations that take no access tokens at all.
	f.do(request{method: "GET", path: "/api/v1/me", bearer: publish.Token}, 403, &p)
	f.do(request{method: "POST", path: "/api/v1/me/tokens", bearer: publish.Token, body: map[string]any{"name": "x", "scope": "read", "expires_in_days": 1}}, 403, &p)
	if p.Code != "access.denied" {
		t.Errorf("token issuing a token: %+v", p)
	}

	// The owner loses the right to edit organisation resources: so does
	// the token, at once.
	ctx := context.Background()
	other := f.invited(t, "second")
	yes, no := true, false
	if _, err := f.access.UpdateUser(ctx, access.Actor{UserID: f.admin.ID, Origin: access.OriginWeb}, other.ID, access.UserChange{Admin: &yes}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.access.UpdateUser(ctx, access.Actor{UserID: other.ID, Origin: access.OriginWeb}, f.admin.ID, access.UserChange{Admin: &no}); err != nil {
		t.Fatal(err)
	}
	f.do(request{method: "GET", path: "/api/v1/resources", bearer: publish.Token}, 403, nil)
	f.do(request{method: "GET", path: "/api/v1/stages", bearer: publish.Token}, 200, nil)

	// Revoked, it is refused.
	f.do(request{method: "DELETE", path: "/api/v1/me/tokens/" + read.AccessToken.Id, cookie: session}, 204, nil)
	f.do(request{method: "GET", path: "/api/v1/stages", bearer: read.Token}, 401, &p)
	f.do(request{method: "DELETE", path: "/api/v1/me/tokens/" + read.AccessToken.Id, cookie: f.sessionFor(t, other.ID)}, 404, nil)

	// Credentials that are not tokens.
	f.do(request{method: "GET", path: "/api/v1/stages", bearer: "rgd_not-a-token"}, 401, nil)
	f.do(request{method: "GET", path: "/api/v1/stages", header: map[string]string{"Authorization": "Basic YWRtaW46YWRtaW4="}}, 401, nil)
	f.do(request{method: "GET", path: "/api/v1/stages", bearer: authn.NewToken(authn.DeviceTokenPrefix)}, 401, nil)
}

func (f *fixture) invited(t *testing.T, name string) access.User {
	t.Helper()
	ctx := context.Background()
	invs, err := f.access.CreateInvites(ctx, access.Actor{UserID: f.admin.ID, Origin: access.OriginWeb}, []string{name + "@example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	in, err := f.authn.AcceptInvite(ctx, invs[0].Token, access.NewAccount{Username: name, DisplayName: name}, "<placeholder: long enough>")
	if err != nil {
		t.Fatal(err)
	}
	return in.User
}

func (f *fixture) sessionFor(t *testing.T, userID string) string {
	t.Helper()
	in, err := f.authn.StartSession(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return in.Token
}

// Cross-site requests cannot use the cookie; bearer tokens are not
// browser sessions and are not checked.
func TestCrossSiteRequestsAreRefused(t *testing.T) {
	f := newFixture(t, authnapi.Options{})
	session := f.login("admin", adminPassword)
	var p httpapi.Problem
	cross := map[string]string{"Sec-Fetch-Site": "cross-site"}
	f.do(request{method: "POST", path: "/api/v1/auth/logout", cookie: session, header: cross}, 403, &p)
	f.do(request{method: "POST", path: "/api/v1/auth/login", header: cross, body: map[string]string{"username": "admin", "password": adminPassword}}, 403, &p)
	// Without Sec-Fetch-Site, the Origin decides.
	f.do(request{method: "POST", path: "/api/v1/auth/logout", cookie: session, header: map[string]string{"Origin": "https://evil.example.com"}}, 403, &p)
	if p.Code != "access.denied" {
		t.Errorf("cross-site: %+v", p)
	}
	// Reading is not protected by it, and the public origin is trusted
	// even when a proxy changed Host.
	f.do(request{method: "GET", path: "/api/v1/me", cookie: session, header: cross}, 200, nil)
	f.do(request{method: "POST", path: "/api/v1/auth/logout", cookie: session, header: map[string]string{"Origin": "https://hub.example.com"}}, 204, nil)
}

func TestSignOutEverywhereOverHTTP(t *testing.T) {
	f := newFixture(t, authnapi.Options{})
	a, b := f.login("admin", adminPassword), f.login("admin", adminPassword)
	rec := f.do(request{method: "POST", path: "/api/v1/me/sign-out-all", cookie: a}, 204, nil)
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("cookie: %v", rec.Header())
	}
	f.do(request{method: "GET", path: "/api/v1/me", cookie: a}, 401, nil)
	f.do(request{method: "GET", path: "/api/v1/me", cookie: b}, 401, nil)
}

// A plain-http base address (a trial on one machine) makes the cookie work
// without TLS; a base path scopes it.
func TestCookieFollowsTheBaseAddress(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:8080/hub")
	f := newFixture(t, authnapi.Options{BaseURL: base})
	rec := f.do(request{method: "POST", path: "/api/v1/auth/login", body: map[string]string{"username": "admin", "password": adminPassword}}, 200, nil)
	set := rec.Header().Get("Set-Cookie")
	if strings.Contains(set, "Secure") || !strings.Contains(set, "Path=/hub;") {
		t.Errorf("Set-Cookie: %q", set)
	}
}
