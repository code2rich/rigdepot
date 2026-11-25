// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/identity"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/httpapi/authnapi"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
)

// fakeSource is an identity source that needs no network: Begin points at idp.example.com and
// Complete returns whoever the test put in next. It checks the state and the client secret like
// a real source, and it is careless on purpose: its errors contain the client secret it was
// given, so that the tests show the core masks it.
type fakeSource struct {
	secret string // the secret the "provider" accepts

	mu   sync.Mutex
	next identity.Identity
}

func (*fakeSource) ID() string   { return "fake" }
func (*fakeSource) Name() string { return "Fake" }
func (*fakeSource) ConfigSchema() []byte {
	return []byte(`{"type":"object","required":["issuer","client_id"],"additionalProperties":false,
		"properties":{"issuer":{"type":"string","format":"uri"},"client_id":{"type":"string","minLength":1}}}`)
}

func (*fakeSource) Validate(cfg identity.Config) error {
	if !strings.HasPrefix(cfg.String("issuer"), "https://") {
		return &identity.ConfigError{Field: "issuer", Code: "https"}
	}
	return nil
}

func (s *fakeSource) Begin(_ context.Context, cfg identity.Config, callbackURL string) (string, []byte, error) {
	state := rand.Text()
	q := url.Values{"state": {state}, "client_id": {cfg.String("client_id")}, "redirect_uri": {callbackURL}}
	return cfg.String("issuer") + "/authorize?" + q.Encode(), []byte(state), nil
}

func (s *fakeSource) Complete(_ context.Context, cfg identity.Config, state []byte, q url.Values) (identity.Identity, error) {
	if q.Get("state") != string(state) {
		return identity.Identity{}, identity.ErrState
	}
	if q.Get("error") == "access_denied" {
		return identity.Identity{}, identity.ErrDeclined
	}
	if cfg.ClientSecret != s.secret {
		return identity.Identity{}, fmt.Errorf("the token endpoint refused client_secret=%s", url.QueryEscape(cfg.ClientSecret))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next, nil
}

func (s *fakeSource) Test(_ context.Context, cfg identity.Config, _ string) error {
	if cfg.ClientSecret != s.secret {
		return &identity.TestError{Step: identity.StepClient, Err: fmt.Errorf("refused the secret %q", cfg.ClientSecret)}
	}
	return nil
}

func (s *fakeSource) signsIn(id identity.Identity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = id
}

// idpFixture is the API with the fake source compiled in and its log kept.
type idpFixture struct {
	*apiFixture
	src    *fakeSource
	secret string
	logs   *bytes.Buffer
	bodies []string // every response body, to search for the secret
}

const fakeSecret = "<placeholder: client secret>-7d1f9c"

func newIdP(t *testing.T) *idpFixture {
	t.Helper()
	logs := &bytes.Buffer{}
	src := &fakeSource{secret: fakeSecret}
	f := newAPIWith(t, fixtureOptions{sources: []identity.Provider{src}, signInRate: authnapi.Rate{Burst: 1000, Every: time.Millisecond}, log: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	return &idpFixture{apiFixture: f, src: src, secret: fakeSecret, logs: logs}
}

func (f *idpFixture) callJSON(t *testing.T, method, path, user string, body any, header map[string]string, want int) (*httptest.ResponseRecorder, httpapi.Problem) {
	t.Helper()
	raw := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(b)
	}
	rec, p := f.call(t, method, path, user, raw, header, want)
	f.bodies = append(f.bodies, rec.Body.String()+fmt.Sprint(rec.Header()))
	return rec, p
}

// createFake adds the fake source as the administrator and returns it.
func (f *idpFixture) createFake(t *testing.T, name string) httpapi.IdentityProvider {
	t.Helper()
	rec, _ := f.callJSON(t, http.MethodPost, "/api/v1/identity-providers", testAdmin, map[string]any{
		"kind": "fake", "name": name, "config": map[string]any{"issuer": "https://idp.example.com", "client_id": "rigdepot"}, "client_secret": f.secret,
	}, nil, http.StatusCreated)
	var p httpapi.IdentityProvider
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// cookies returns the Set-Cookie values of a response by cookie name.
func cookies(rec *httptest.ResponseRecorder) map[string]*http.Cookie {
	out := map[string]*http.Cookie{}
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		out[c.Name] = c
	}
	return out
}

// login plays the browser: start, the provider's redirect back (with extra query parameters),
// the callback. It returns the callback's response.
func (f *idpFixture) login(t *testing.T, providerID, returnTo string, extra url.Values, tamper func(cookie string, q url.Values) (string, url.Values)) *httptest.ResponseRecorder {
	t.Helper()
	start := "/api/v1/auth/providers/" + providerID + "/start"
	if returnTo != "" {
		start += "?return_to=" + url.QueryEscape(returnTo)
	}
	rec, _ := f.call(t, http.MethodGet, start, "", "", nil, http.StatusFound)
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || loc.Host != "idp.example.com" {
		t.Fatalf("start sends the browser to %q", rec.Header().Get("Location"))
	}
	state := cookies(rec)[authnapi.LoginStateCookie]
	if state == nil || !state.HttpOnly || state.SameSite != http.SameSiteLaxMode || state.Path != "/api/v1/auth/providers/" {
		t.Fatalf("the sign-in state cookie: %+v", state)
	}
	q := url.Values{"code": {"code-1"}, "state": {loc.Query().Get("state")}}
	for k, v := range extra {
		q[k] = v
	}
	cookie := state.Value
	if tamper != nil {
		cookie, q = tamper(cookie, q)
	}
	header := map[string]string{}
	if cookie != "" {
		header["Cookie"] = authnapi.LoginStateCookie + "=" + cookie
	}
	rec, _ = f.call(t, http.MethodGet, "/api/v1/auth/providers/"+providerID+"/callback?"+q.Encode(), "", "", header, http.StatusFound)
	f.bodies = append(f.bodies, rec.Body.String()+fmt.Sprint(rec.Header()))
	if c := cookies(rec)[authnapi.LoginStateCookie]; c == nil || c.MaxAge >= 0 {
		t.Errorf("the callback does not remove the sign-in state cookie: %v", rec.Header()["Set-Cookie"])
	}
	return rec
}

// session returns the session cookie a callback set; "" when none.
func session(rec *httptest.ResponseRecorder) string {
	if c := cookies(rec)[authnapi.SessionCookie]; c != nil && c.MaxAge > 0 {
		return c.Value
	}
	return ""
}

func (f *idpFixture) me(t *testing.T, sess string) httpapi.Me {
	t.Helper()
	rec, _ := f.call(t, http.MethodGet, "/api/v1/me", "", "", map[string]string{"Cookie": authnapi.SessionCookie + "=" + sess}, http.StatusOK)
	var me httpapi.Me
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	return me
}

func wantLocation(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if got := rec.Header().Get("Location"); got != "http://127.0.0.1:8080"+want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// TestIdentityProviderManagement: the kinds, adding, reading, changing, testing and removing
// sign-in methods, by platform administrators from the browser only, and the rule that one
// method stays enabled.
func TestIdentityProviderManagement(t *testing.T) {
	f := newIdP(t)

	rec, _ := f.callJSON(t, http.MethodGet, "/api/v1/identity-providers/kinds", testAdmin, nil, nil, http.StatusOK)
	var kinds httpapi.IdentityProviderKindList
	if err := json.Unmarshal(rec.Body.Bytes(), &kinds); err != nil || len(kinds.Items) != 1 || kinds.Items[0].Kind != "fake" || kinds.Items[0].ConfigSchema["type"] != "object" {
		t.Fatalf("kinds: %s", rec.Body)
	}
	f.callJSON(t, http.MethodGet, "/api/v1/identity-providers/kinds", testMember, nil, nil, http.StatusForbidden)
	f.callJSON(t, http.MethodGet, "/api/v1/identity-providers", testMember, nil, nil, http.StatusForbidden)

	// Settings that do not fit are refused, the secret among the settings too.
	for name, body := range map[string]map[string]any{
		"unknown kind":        {"kind": "saml", "name": "x", "config": map[string]any{}},
		"missing setting":     {"kind": "fake", "name": "x", "config": map[string]any{"issuer": "https://idp.example.com"}},
		"secret in settings":  {"kind": "fake", "name": "x", "config": map[string]any{"issuer": "https://idp.example.com", "client_id": "c", "client_secret": f.secret}},
		"plain http":          {"kind": "fake", "name": "x", "config": map[string]any{"issuer": "http://idp.example.com", "client_id": "c"}},
		"name too long":       {"kind": "fake", "name": strings.Repeat("x", 65), "config": map[string]any{"issuer": "https://idp.example.com", "client_id": "c"}},
		"setting of a number": {"kind": "fake", "name": "x", "config": map[string]any{"issuer": 5, "client_id": "c"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, p := f.callJSON(t, http.MethodPost, "/api/v1/identity-providers", testAdmin, body, nil, http.StatusBadRequest)
			if p.Code != "request.invalid" || p.Errors == nil {
				t.Errorf("problem: %+v", p)
			}
		})
	}
	// Only from the browser, and only administrators.
	f.callJSON(t, http.MethodPost, "/api/v1/identity-providers", testMember, map[string]any{"kind": "fake", "name": "x", "config": map[string]any{}}, nil, http.StatusForbidden)
	f.callJSON(t, http.MethodPost, "/api/v1/identity-providers", "", map[string]any{"kind": "fake", "name": "x", "config": map[string]any{}},
		map[string]string{"Authorization": "Bearer " + f.deviceToken}, http.StatusForbidden)

	p := f.createFake(t, "Company SSO")
	if !p.HasClientSecret || !p.Enabled || p.Kind != "fake" || p.Config["client_id"] != "rigdepot" ||
		p.CallbackUrl == nil || *p.CallbackUrl != "http://127.0.0.1:8080/api/v1/auth/providers/"+p.Id+"/callback" {
		t.Errorf("created: %+v", p)
	}

	rec, _ = f.callJSON(t, http.MethodGet, "/api/v1/identity-providers", testAdmin, nil, nil, http.StatusOK)
	var list httpapi.IdentityProviderList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 2 || list.Items[0].Id != "local" || list.Items[1].Id != p.Id {
		t.Fatalf("list: %s", rec.Body)
	}
	// Device tokens may read.
	f.callJSON(t, http.MethodGet, "/api/v1/identity-providers/"+p.Id, "", nil, map[string]string{"Authorization": "Bearer " + f.deviceToken}, http.StatusOK)

	path := "/api/v1/identity-providers/" + p.Id
	f.callJSON(t, http.MethodPatch, path, testAdmin, map[string]any{"name": "SSO"}, nil, http.StatusPreconditionRequired)
	f.callJSON(t, http.MethodPatch, path, testAdmin, map[string]any{"name": "SSO"}, map[string]string{"If-Match": `"p-x-1"`}, http.StatusPreconditionFailed)
	rec, _ = f.callJSON(t, http.MethodPatch, path, testAdmin, map[string]any{"name": "SSO"}, map[string]string{"If-Match": p.Etag}, http.StatusOK)
	var changed httpapi.IdentityProvider
	if err := json.Unmarshal(rec.Body.Bytes(), &changed); err != nil || changed.Name != "SSO" || changed.Etag == p.Etag || rec.Header().Get("ETag") != changed.Etag || !changed.HasClientSecret {
		t.Errorf("changed: %s", rec.Body)
	}

	// Testing: right secret, then a wrong one whose refusal echoes it, masked.
	rec, _ = f.callJSON(t, http.MethodPost, path+"/test", testAdmin, nil, nil, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Errorf("test: %s", rec.Body)
	}
	wrong := "wrong-" + f.secret
	f.callJSON(t, http.MethodPatch, path, testAdmin, map[string]any{"client_secret": wrong}, map[string]string{"If-Match": changed.Etag}, http.StatusOK)
	rec, _ = f.callJSON(t, http.MethodPost, path+"/test", testAdmin, nil, nil, http.StatusOK)
	var res httpapi.IdentityProviderTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.Ok || res.Step == nil || *res.Step != "client" || res.Detail == nil {
		t.Errorf("test with a wrong secret: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), wrong) || res.Detail == nil || !strings.Contains(*res.Detail, "<client secret>") {
		t.Errorf("the test result shows the secret: %s", rec.Body)
	}
	f.callJSON(t, http.MethodPost, path+"/test", testMember, nil, nil, http.StatusForbidden)

	// One method stays on.
	rec, _ = f.callJSON(t, http.MethodGet, "/api/v1/identity-providers/local", testAdmin, nil, nil, http.StatusOK)
	var local httpapi.IdentityProvider
	_ = json.Unmarshal(rec.Body.Bytes(), &local)
	rec, _ = f.callJSON(t, http.MethodPatch, "/api/v1/identity-providers/local", testAdmin, map[string]any{"enabled": false}, map[string]string{"If-Match": local.Etag}, http.StatusOK)
	_ = json.Unmarshal(rec.Body.Bytes(), &local)
	rec, _ = f.callJSON(t, http.MethodGet, path, testAdmin, nil, nil, http.StatusOK)
	_ = json.Unmarshal(rec.Body.Bytes(), &changed)
	if _, prob := f.callJSON(t, http.MethodPatch, path, testAdmin, map[string]any{"enabled": false}, map[string]string{"If-Match": changed.Etag}, http.StatusConflict); prob.Code != "identity.last_method" {
		t.Errorf("turning off the last method: %+v", prob)
	}
	if _, prob := f.callJSON(t, http.MethodDelete, path, testAdmin, nil, nil, http.StatusConflict); prob.Code != "identity.last_method" {
		t.Errorf("removing the last method: %+v", prob)
	}
	if _, prob := f.callJSON(t, http.MethodPatch, "/api/v1/identity-providers/local", testAdmin, map[string]any{"name": "x"}, map[string]string{"If-Match": local.Etag}, http.StatusConflict); prob.Code != "identity.local_fixed" {
		t.Errorf("renaming local accounts: %+v", prob)
	}
	f.callJSON(t, http.MethodPatch, "/api/v1/identity-providers/local", testAdmin, map[string]any{"enabled": true}, map[string]string{"If-Match": local.Etag}, http.StatusOK)
	if _, prob := f.callJSON(t, http.MethodDelete, "/api/v1/identity-providers/local", testAdmin, nil, nil, http.StatusConflict); prob.Code != "identity.local_fixed" {
		t.Errorf("removing local accounts: %+v", prob)
	}
	f.callJSON(t, http.MethodDelete, path, testMember, nil, nil, http.StatusForbidden)
	f.callJSON(t, http.MethodDelete, path, testAdmin, nil, nil, http.StatusNoContent)
	f.callJSON(t, http.MethodGet, path, testAdmin, nil, nil, http.StatusNotFound)

	f.noSecretAnywhere(t, f.secret, wrong)
}

// noSecretAnywhere searches every response, the log, the audit log (as the API exports it) and
// the database file for the secrets, in plain and in the forms they travel in.
func (f *idpFixture) noSecretAnywhere(t *testing.T, secrets ...string) {
	t.Helper()
	rec, _ := f.call(t, http.MethodGet, "/api/v1/audit/export", testAdmin, "", nil, http.StatusOK)
	audit := rec.Body.String()
	if !strings.Contains(audit, "identity_provider.") {
		t.Errorf("the audit log has no entries about sign-in methods:\n%s", audit)
	}
	db, err := os.ReadFile(filepath.Join(f.app.cfg.DataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secrets {
		for _, form := range []string{s, url.QueryEscape(s)} {
			for i, b := range f.bodies {
				if strings.Contains(b, form) {
					t.Errorf("response %d shows a client secret: %s", i, b)
				}
			}
			if strings.Contains(f.logs.String(), form) {
				t.Errorf("the log shows a client secret:\n%s", f.logs)
			}
			if strings.Contains(audit, form) {
				t.Errorf("the audit log shows a client secret")
			}
			if bytes.Contains(db, []byte(form)) {
				t.Errorf("the database holds a client secret in plain text")
			}
		}
	}
}

// TestProviderLoginLinksAccounts walks the sign-in through an identity source over HTTP: a new
// person, the same person again, an existing account linked by its verified address, an address
// the source did not verify, a disabled account, and the ways a callback must fail.
func TestProviderLoginLinksAccounts(t *testing.T) {
	f := newIdP(t)
	ctx := context.Background()
	p := f.createFake(t, "Company SSO")

	rec, _ := f.call(t, http.MethodGet, "/api/v1/meta", "", "", nil, http.StatusOK)
	var meta httpapi.ServerMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil || len(meta.LoginMethods) != 2 || meta.LoginMethods[1] != (httpapi.LoginMethod{Id: p.Id, Kind: "fake", Name: "Company SSO"}) {
		t.Fatalf("meta: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "rigdepot\"") || strings.Contains(rec.Body.String(), "idp.example.com") {
		t.Errorf("meta shows a source's settings: %s", rec.Body)
	}

	t.Run("someone new gets a pending account", func(t *testing.T) {
		f.src.signsIn(identity.Identity{Subject: "s-new", Email: "New.Person@example.com", EmailVerified: true, Username: "new.person", DisplayName: "New Person"})
		rec := f.login(t, p.Id, "/catalog", nil, nil)
		wantLocation(t, rec, "/apply")
		me := f.me(t, session(rec))
		if me.Status != "pending" || me.Username != "new.person" || me.DisplayName != "New Person" || me.Email == nil || string(*me.Email) != "new.person@example.com" {
			t.Errorf("me = %+v", me)
		}
		// The same person again: the same account.
		rec = f.login(t, p.Id, "", nil, nil)
		if again := f.me(t, session(rec)); again.Id != me.Id {
			t.Errorf("the second sign-in made another account: %s, %s", again.Id, me.Id)
		}
	})

	if _, err := f.app.db.ExecContext(ctx, `UPDATE users SET email = 'jdoe@example.com' WHERE id = ?`, testMember); err != nil {
		t.Fatal(err)
	}
	t.Run("an address the source did not verify links nothing", func(t *testing.T) {
		f.src.signsIn(identity.Identity{Subject: "s-unverified", Email: "jdoe@example.com", EmailVerified: false, Username: "jdoe"})
		rec := f.login(t, p.Id, "", nil, nil)
		me := f.me(t, session(rec))
		if me.Id == testMember || me.Status != "pending" || me.Email != nil || me.Username == "jdoe" {
			t.Errorf("an unverified address signed in as %+v", me)
		}
	})
	t.Run("a verified address links the existing account", func(t *testing.T) {
		f.src.signsIn(identity.Identity{Subject: "s-jdoe", Email: "JDoe@example.com", EmailVerified: true})
		rec := f.login(t, p.Id, "/catalog?q=1", nil, nil)
		wantLocation(t, rec, "/catalog?q=1")
		if me := f.me(t, session(rec)); me.Id != testMember {
			t.Errorf("signed in as %s, want the existing account", me.Id)
		}
	})
	t.Run("a return address on another site is ignored", func(t *testing.T) {
		for _, bad := range []string{"//evil.example.com/x", "https://evil.example.com/", `/\evil.example.com`} {
			rec := f.login(t, p.Id, bad, nil, nil)
			wantLocation(t, rec, "/")
		}
	})
	t.Run("a disabled account is refused", func(t *testing.T) {
		if _, err := f.app.db.ExecContext(ctx, `UPDATE users SET status = 'disabled' WHERE id = ?`, testMember); err != nil {
			t.Fatal(err)
		}
		defer f.app.db.ExecContext(ctx, `UPDATE users SET status = 'active' WHERE id = ?`, testMember)
		// Linked by the identity, and by the address.
		for _, id := range []identity.Identity{{Subject: "s-jdoe"}, {Subject: "s-jdoe-2", Email: "jdoe@example.com", EmailVerified: true}} {
			f.src.signsIn(id)
			rec := f.login(t, p.Id, "", nil, nil)
			wantLocation(t, rec, "/login?error=user.disabled")
			if session(rec) != "" {
				t.Error("a disabled account got a session")
			}
		}
	})
	t.Run("an address two accounts have is for an administrator", func(t *testing.T) {
		if _, err := f.app.db.ExecContext(ctx, `UPDATE users SET email = 'jdoe@example.com' WHERE id = ?`, testAdmin); err != nil {
			t.Fatal(err)
		}
		defer f.app.db.ExecContext(ctx, `UPDATE users SET email = '' WHERE id = ?`, testAdmin)
		f.src.signsIn(identity.Identity{Subject: "s-twice", Email: "jdoe@example.com", EmailVerified: true})
		rec := f.login(t, p.Id, "", nil, nil)
		wantLocation(t, rec, "/login?error=auth.email_conflict")
	})
	t.Run("failed callbacks", func(t *testing.T) {
		f.src.signsIn(identity.Identity{Subject: "s-new"})
		for name, tc := range map[string]struct {
			extra  url.Values
			tamper func(string, url.Values) (string, url.Values)
			code   string
		}{
			"declined":                   {extra: url.Values{"error": {"access_denied"}}, code: "auth.declined"},
			"no state cookie":            {tamper: func(_ string, q url.Values) (string, url.Values) { return "", q }, code: "auth.state_invalid"},
			"a forged state cookie":      {tamper: func(c string, q url.Values) (string, url.Values) { return "x" + c[1:], q }, code: "auth.state_invalid"},
			"another sign-in's callback": {tamper: func(c string, q url.Values) (string, url.Values) { q.Set("state", "other"); return c, q }, code: "auth.state_invalid"},
		} {
			t.Run(name, func(t *testing.T) {
				rec := f.login(t, p.Id, "", tc.extra, tc.tamper)
				wantLocation(t, rec, "/login?error="+tc.code)
				if session(rec) != "" {
					t.Error("a failed callback set a session")
				}
			})
		}
	})
	t.Run("a cookie of another source", func(t *testing.T) {
		other := f.createFake(t, "Other SSO")
		rec, _ := f.call(t, http.MethodGet, "/api/v1/auth/providers/"+other.Id+"/start", "", "", nil, http.StatusFound)
		cookie := cookies(rec)[authnapi.LoginStateCookie].Value
		loc, _ := url.Parse(rec.Header().Get("Location"))
		rec, _ = f.call(t, http.MethodGet, "/api/v1/auth/providers/"+p.Id+"/callback?code=c&state="+loc.Query().Get("state"), "", "",
			map[string]string{"Cookie": authnapi.LoginStateCookie + "=" + cookie}, http.StatusFound)
		wantLocation(t, rec, "/login?error=auth.state_invalid")
	})
	t.Run("a source that is off", func(t *testing.T) {
		rec, _ := f.call(t, http.MethodGet, "/api/v1/auth/providers/no-such-source/start", "", "", nil, http.StatusFound)
		wantLocation(t, rec, "/login?error=auth.provider_unavailable")
		rec, _ = f.call(t, http.MethodGet, "/api/v1/auth/providers/local/start", "", "", nil, http.StatusFound)
		wantLocation(t, rec, "/login?error=auth.provider_unavailable")
	})
	t.Run("a wrong client secret refuses the sign-in", func(t *testing.T) {
		f.callJSON(t, http.MethodPatch, "/api/v1/identity-providers/"+p.Id, testAdmin, map[string]any{"client_secret": "wrong-" + f.secret}, map[string]string{"If-Match": "*"}, http.StatusOK)
		defer f.callJSON(t, http.MethodPatch, "/api/v1/identity-providers/"+p.Id, testAdmin, map[string]any{"client_secret": f.secret}, map[string]string{"If-Match": "*"}, http.StatusOK)
		rec := f.login(t, p.Id, "", nil, nil)
		wantLocation(t, rec, "/login?error=auth.provider_failed")
		if !strings.Contains(f.logs.String(), "<client secret>") {
			t.Errorf("the log does not show the masked cause:\n%s", f.logs)
		}
	})
	t.Run("rotating the master key keeps the client secret", func(t *testing.T) {
		rotated, err := f.app.keys.Rotate("")
		if err != nil {
			t.Fatal(err)
		}
		counts, err := masterkey.RewrapAll(ctx, f.app.db, rotated, rewrappers())
		if err != nil || counts["identity_providers"] != 2 {
			t.Fatalf("rewrap: %v %v", counts, err)
		}
		f.src.signsIn(identity.Identity{Subject: "s-new"})
		if rec := f.login(t, p.Id, "", nil, nil); session(rec) == "" {
			t.Errorf("no sign-in after the rotation: %s", rec.Header().Get("Location"))
		}
	})
	t.Run("password sign-in can be turned off", func(t *testing.T) {
		f.callJSON(t, http.MethodPatch, "/api/v1/identity-providers/local", testAdmin, map[string]any{"enabled": false}, map[string]string{"If-Match": "*"}, http.StatusOK)
		_, prob := f.callJSON(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "<placeholder: password>"}, nil, http.StatusForbidden)
		if prob.Code != "auth.method_disabled" {
			t.Errorf("login with local accounts off: %+v", prob)
		}
		rec, _ := f.call(t, http.MethodGet, "/api/v1/meta", "", "", nil, http.StatusOK)
		if strings.Contains(rec.Body.String(), `"id":"local"`) {
			t.Errorf("meta lists local accounts that are off: %s", rec.Body)
		}
	})

	// Every sign-in is in the audit log, the refused ones too.
	rec, _ = f.call(t, http.MethodGet, "/api/v1/audit/export?category=login", testAdmin, "", nil, http.StatusOK)
	for _, want := range []string{"auth.login", "fake", "state_invalid", "declined", "disabled", "email_conflict", "provider_failed"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the audit log lacks %q:\n%s", want, rec.Body)
		}
	}
	f.noSecretAnywhere(t, f.secret, "wrong-"+f.secret)
}

// TestBrokenSettings: a source whose stored settings cannot be read sends the browser back to the
// login page; it is not a 500, and it does not take the login page down.
func TestBrokenSettings(t *testing.T) {
	f := newIdP(t)
	p := f.createFake(t, "SSO")
	if _, err := f.app.db.ExecContext(context.Background(), `UPDATE identity_providers SET config = '{"issuer":"https://idp.example.com"' WHERE id = ?`, p.Id); err != nil {
		t.Fatal(err)
	}
	// Settings that do not parse are unknown: the source is not used, and not offered, but
	// administrators still see it, to fix or remove it.
	rec, _ := f.call(t, http.MethodGet, "/api/v1/auth/providers/"+p.Id+"/start", "", "", nil, http.StatusFound)
	wantLocation(t, rec, "/login?error=auth.provider_failed")
	rec, _ = f.call(t, http.MethodGet, "/api/v1/meta", "", "", nil, http.StatusOK)
	if strings.Contains(rec.Body.String(), p.Id) {
		t.Errorf("meta offers a source whose settings are broken: %s", rec.Body)
	}
	rec, _ = f.call(t, http.MethodGet, "/api/v1/identity-providers", testAdmin, "", nil, http.StatusOK)
	if !strings.Contains(rec.Body.String(), p.Id) {
		t.Errorf("the list hides a source whose settings are broken: %s", rec.Body)
	}
	f.call(t, http.MethodDelete, "/api/v1/identity-providers/"+p.Id, testAdmin, "", nil, http.StatusNoContent)
}
