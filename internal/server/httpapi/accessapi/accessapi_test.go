// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package accessapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/httpapi/accessapi"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// nobody stands in for the operations of the other domains. It sits one
// level deeper than *accessapi.Handlers, so the handlers' methods win; any
// other operation panics, which a test would notice.
type nobody struct{ httpapi.StrictServerInterface }

type server struct {
	*accessapi.Handlers
	nobody
}

var _ httpapi.StrictServerInterface = server{}

type auditor struct {
	mu      sync.Mutex
	entries []access.AuditEntry
}

func (a *auditor) Append(_ context.Context, _ *dbx.Tx, e access.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}

// fixture is a server on a fresh SQLite database with a cast:
// admin, owner (of shop), member (of shop), pending.
type fixture struct {
	t      *testing.T
	h      http.Handler
	svc    *access.Service
	db     *dbx.DB
	users  map[string]access.User
	router routers.Router
}

func newFixture(t *testing.T, o accessapi.Options) *fixture {
	t.Helper()
	ctx := context.Background()
	db := dbxtest.Open(t, dbxtest.SQLiteURL(t))
	if _, err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	svc := access.New(db, access.Options{Auditor: &auditor{}})
	f := &fixture{t: t, svc: svc, db: db, users: map[string]access.User{}}
	for _, c := range []struct {
		name   string
		status access.Status
		admin  bool
	}{{"admin", access.StatusActive, true}, {"owner", access.StatusActive, false}, {"member", access.StatusActive, false}, {"pending", access.StatusPending, false}} {
		err := db.InTx(ctx, func(tx *dbx.Tx) error {
			u, err := svc.CreateUser(ctx, tx, access.System, access.NewUser{
				Username: c.name, DisplayName: "Name of " + c.name, Email: c.name + "@example.com", Status: c.status, Admin: c.admin,
			})
			f.users[c.name] = u
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.CreateProduct(ctx, access.System, access.NewProduct{Code: "shop", Name: "Shop", Line: "Commerce", Owners: []string{f.users["owner"].ID}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.PutMember(ctx, access.System, "shop", f.users["member"].ID, access.RoleMember); err != nil {
		t.Fatal(err)
	}

	impl := server{Handlers: accessapi.New(svc, o)}
	inner := httpapi.NewHandler(impl, http.NewServeMux())
	// Stands in for the authentication middleware of package authn: the
	// test names the user in a header.
	f.h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name := r.Header.Get("X-Test-User"); name != "" {
			r = r.WithContext(access.WithActor(r.Context(), access.Actor{UserID: f.users[name].ID, Origin: access.OriginWeb}))
		}
		inner.ServeHTTP(w, r)
	})

	f.router = contract(t)
	return f
}

var (
	contractOnce   sync.Once
	contractRouter routers.Router
	contractErr    error
)

// contract returns a router over api/openapi.yaml, loaded once.
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

// do sends a request as user (empty: not signed in), checks the response
// against the contract and decodes it into out when out is not nil.
func (f *fixture) do(user, method, path string, body any, wantStatus int, out any) {
	f.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			f.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		f.t.Fatalf("%s %s as %q: %d, want %d\n%s", method, path, user, rec.Code, wantStatus, rec.Body)
	}
	f.conforms(req, raw, rec)
	if out != nil {
		// Decode into a zero value: json keeps fields the body leaves out.
		reflect.ValueOf(out).Elem().SetZero()
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			f.t.Fatalf("%s %s: %v\n%s", method, path, err, rec.Body)
		}
	}
}

// conforms fails the test when the response is not what the contract says
// the operation answers.
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

func TestProductsOverHTTP(t *testing.T) {
	f := newFixture(t, accessapi.Options{
		ResourceCounts: func(context.Context) (map[string]int, error) { return map[string]int{}, nil },
	})
	var list httpapi.ProductList
	f.do("pending", "GET", "/api/v1/products", nil, 200, &list)
	if len(list.Items) != 1 || list.Items[0].Code != "shop" || list.Items[0].MemberCount != 2 || list.Items[0].MyRole != nil {
		t.Errorf("products as pending: %+v", list)
	}
	var p httpapi.Product
	f.do("member", "GET", "/api/v1/products/shop", nil, 200, &p)
	if p.MyRole == nil || *p.MyRole != "member" || len(p.Owners) != 1 || p.Owners[0].Username != "owner" {
		t.Errorf("shop as member: %+v", p)
	}
	f.do("member", "GET", "/api/v1/products/nope", nil, 404, nil)
	f.do("", "GET", "/api/v1/products", nil, 401, nil)

	var prob httpapi.Problem
	f.do("owner", "POST", "/api/v1/products", map[string]any{"code": "pay", "name": "Payments"}, 403, &prob)
	if prob.Code != "access.denied" {
		t.Errorf("an owner creating: %+v", prob)
	}
	f.do("admin", "POST", "/api/v1/products", map[string]any{"code": "sync", "name": "Sync"}, 409, &prob)
	if prob.Code != "product.code_reserved" || prob.Params == nil || (*prob.Params)["code"] != "sync" {
		t.Errorf("a reserved code: %+v", prob)
	}
	f.do("admin", "POST", "/api/v1/products", map[string]any{"code": "shop", "name": "Again"}, 409, &prob)
	if prob.Code != "product.code_taken" {
		t.Errorf("a taken code: %+v", prob)
	}
	f.do("admin", "POST", "/api/v1/products", map[string]any{"code": "P", "name": ""}, 400, &prob)
	if prob.Code != "request.invalid" || prob.Errors == nil || len(*prob.Errors) != 2 {
		t.Errorf("invalid fields: %+v", prob)
	}
	f.do("admin", "POST", "/api/v1/products", map[string]any{"code": "pay", "name": "Payments", "line": "Commerce", "owners": []string{f.users["member"].ID}}, 201, &p)
	if p.Code != "pay" || len(p.Owners) != 1 || p.Line == nil || *p.Line != "Commerce" {
		t.Errorf("created: %+v", p)
	}
	f.do("admin", "PATCH", "/api/v1/products/pay", map[string]any{"name": "Pay"}, 200, &p)
	if p.Name != "Pay" {
		t.Errorf("renamed: %+v", p)
	}
}

func TestMembersOverHTTP(t *testing.T) {
	f := newFixture(t, accessapi.Options{})
	var ms httpapi.MemberList
	f.do("member", "GET", "/api/v1/products/shop/members", nil, 200, &ms)
	if len(ms.Items) != 2 || ms.Items[0].Role != "owner" {
		t.Errorf("members: %+v", ms)
	}
	f.do("pending", "GET", "/api/v1/products/shop/members", nil, 403, nil)

	pending := f.users["pending"].ID
	var m httpapi.Member
	f.do("owner", "PUT", "/api/v1/products/shop/members/"+pending, map[string]any{"role": "member"}, 201, &m)
	if m.User.Id != pending || m.Role != "member" {
		t.Errorf("added: %+v", m)
	}
	f.do("owner", "PUT", "/api/v1/products/shop/members/"+pending, map[string]any{"role": "owner"}, 200, &m)
	if m.Role != "owner" {
		t.Errorf("promoted: %+v", m)
	}
	f.do("owner", "PUT", "/api/v1/products/shop/members/"+pending, map[string]any{"role": "boss"}, 400, nil)
	f.do("member", "DELETE", "/api/v1/products/shop/members/"+pending, nil, 403, nil)
	f.do("owner", "DELETE", "/api/v1/products/shop/members/"+pending, nil, 204, nil)

	var prob httpapi.Problem
	f.do("owner", "DELETE", "/api/v1/products/shop/members/"+f.users["owner"].ID, nil, 409, &prob)
	if prob.Code != "product.last_owner" || prob.Detail == nil {
		t.Errorf("last owner: %+v", prob)
	}
	f.do("owner", "PUT", "/api/v1/products/shop/members/"+f.users["owner"].ID, map[string]any{"role": "member"}, 409, &prob)
	if prob.Code != "product.last_owner" {
		t.Errorf("last owner stepping down: %+v", prob)
	}
}

func TestUsersOverHTTP(t *testing.T) {
	last := time.Date(2026, 11, 25, 10, 12, 0, 0, time.UTC)
	f := newFixture(t, accessapi.Options{
		SignIns: func(_ context.Context, ids []string) (map[string]accessapi.SignIn, error) {
			out := map[string]accessapi.SignIn{}
			for _, id := range ids {
				out[id] = accessapi.SignIn{Methods: []string{"local"}, LastLoginAt: &last}
			}
			return out, nil
		},
	})
	var list httpapi.UserList
	f.do("admin", "GET", "/api/v1/users?q=member", nil, 200, &list)
	if len(list.Items) != 1 || list.Items[0].Username != "member" || len(list.Items[0].Identities) != 1 ||
		list.Items[0].LastLoginAt == nil || len(list.Items[0].Memberships) != 1 {
		t.Errorf("search: %+v", list)
	}
	f.do("owner", "GET", "/api/v1/users?status=pending", nil, 200, &list)
	if len(list.Items) != 1 || list.Items[0].Status != "pending" {
		t.Errorf("pending ones: %+v", list)
	}
	f.do("member", "GET", "/api/v1/users", nil, 403, nil)
	f.do("member", "GET", "/api/v1/users/"+f.users["member"].ID, nil, 200, nil)
	f.do("member", "GET", "/api/v1/users/not-an-id", nil, 400, nil)

	var u httpapi.User
	f.do("admin", "PATCH", "/api/v1/users/"+f.users["member"].ID, map[string]any{"status": "disabled"}, 200, &u)
	if u.Status != "disabled" {
		t.Errorf("disabled: %+v", u)
	}
	// Disabled takes effect with the next request.
	f.do("member", "GET", "/api/v1/products", nil, 403, nil)
	var prob httpapi.Problem
	f.do("admin", "PATCH", "/api/v1/users/"+f.users["admin"].ID, map[string]any{"platform_admin": false}, 409, &prob)
	if prob.Code != "user.last_admin" {
		t.Errorf("last admin: %+v", prob)
	}
	f.do("owner", "PATCH", "/api/v1/users/"+f.users["owner"].ID, map[string]any{"platform_admin": true}, 403, nil)
}

func TestMeOverHTTP(t *testing.T) {
	f := newFixture(t, accessapi.Options{})
	var me httpapi.Me
	f.do("pending", "GET", "/api/v1/me", nil, 200, &me)
	if me.Status != "pending" || len(me.Memberships) != 0 {
		t.Errorf("me: %+v", me)
	}
	f.do("pending", "PATCH", "/api/v1/me", map[string]any{"job": "Developer", "focus_stages": []string{"design", "develop"}, "locale": "en"}, 200, &me)
	if me.Job == nil || *me.Job != "Developer" || len(me.FocusStages) != 2 || me.Locale != "en" {
		t.Errorf("updated: %+v", me)
	}
	f.do("pending", "PATCH", "/api/v1/me", map[string]any{"focus_stages": []string{"Not A Stage"}}, 400, nil)
	f.do("", "GET", "/api/v1/me", nil, 401, nil)
}

func TestJoinRequestsOverHTTP(t *testing.T) {
	f := newFixture(t, accessapi.Options{})
	var list httpapi.JoinRequestList
	f.do("pending", "POST", "/api/v1/me/join-requests", map[string]any{"products": []string{"shop"}, "note": "Joining the shop backend team"}, 201, &list)
	if len(list.Items) != 1 || list.Items[0].Status != "pending" {
		t.Fatalf("asked: %+v", list)
	}
	id := list.Items[0].Id
	f.do("pending", "GET", "/api/v1/me/join-requests", nil, 200, &list)
	if len(list.Items) != 1 {
		t.Errorf("mine: %+v", list)
	}
	f.do("member", "POST", "/api/v1/me/join-requests", map[string]any{"products": []string{"shop"}}, 409, nil)
	f.do("owner", "GET", "/api/v1/join-requests", nil, 200, &list)
	if len(list.Items) != 1 || list.Items[0].Id != id {
		t.Errorf("owner's queue: %+v", list)
	}
	f.do("member", "GET", "/api/v1/join-requests?product=shop", nil, 403, nil)
	f.do("member", "GET", "/api/v1/join-requests", nil, 200, &list)
	if len(list.Items) != 0 {
		t.Errorf("a member sees requests: %+v", list)
	}
	f.do("member", "POST", "/api/v1/join-requests/"+id+"/approve", nil, 403, nil)
	var j httpapi.JoinRequest
	f.do("owner", "POST", "/api/v1/join-requests/"+id+"/approve", nil, 200, &j)
	if j.Status != "approved" || j.DecidedBy == nil {
		t.Errorf("approved: %+v", j)
	}
	f.do("owner", "POST", "/api/v1/join-requests/"+id+"/reject", map[string]any{"reason": "Too late"}, 409, nil)
	var me httpapi.Me
	f.do("pending", "GET", "/api/v1/me", nil, 200, &me)
	if me.Status != "active" || len(me.Memberships) != 1 {
		t.Errorf("after approval: %+v", me)
	}
}

func TestInvitesOverHTTP(t *testing.T) {
	base, _ := url.Parse("https://hub.example.com/rigs")
	f := newFixture(t, accessapi.Options{BaseURL: base})
	var list httpapi.InviteList
	f.do("owner", "POST", "/api/v1/invites", map[string]any{"emails": []string{"new@example.com"}}, 403, nil)
	f.do("admin", "POST", "/api/v1/invites", map[string]any{"emails": []string{"new.colleague@example.com"}, "products": []string{"shop"}}, 201, &list)
	if len(list.Items) != 1 || list.Items[0].Link == nil {
		t.Fatalf("invited: %+v", list)
	}
	link := *list.Items[0].Link
	if !strings.HasPrefix(link, "https://hub.example.com/rigs/invite/") {
		t.Fatalf("link: %s", link)
	}
	token := strings.TrimPrefix(link, "https://hub.example.com/rigs/invite/")

	var pv httpapi.InvitePreview
	f.do("", "GET", "/api/v1/auth/invites/"+token, nil, 200, &pv)
	if string(pv.Email) != "new.colleague@example.com" || len(pv.Products) != 1 {
		t.Errorf("preview: %+v", pv)
	}
	f.do("", "GET", "/api/v1/auth/invites/"+strings.Repeat("x", 43), nil, 404, nil)

	f.do("admin", "GET", "/api/v1/invites", nil, 200, &list)
	if len(list.Items) != 1 || list.Items[0].Link != nil {
		t.Errorf("listed: %+v", list)
	}
	f.do("admin", "DELETE", "/api/v1/invites/"+list.Items[0].Id, nil, 204, nil)
	var prob httpapi.Problem
	f.do("", "GET", "/api/v1/auth/invites/"+token, nil, 410, &prob)
	if prob.Code != "invite.expired" {
		t.Errorf("withdrawn: %+v", prob)
	}

	// With mail set up, the link goes by mail and not into the response.
	var mailed []string
	f2 := newFixture(t, accessapi.Options{BaseURL: base, MailInvite: func(_ context.Context, inv access.Invite, link string) error {
		mailed = append(mailed, inv.Email+" "+link)
		return nil
	}})
	f2.do("admin", "POST", "/api/v1/invites", map[string]any{"emails": []string{"a@example.com"}}, 201, &list)
	if list.Items[0].Link != nil || len(mailed) != 1 {
		t.Errorf("mailed: %+v %v", list, mailed)
	}
}
