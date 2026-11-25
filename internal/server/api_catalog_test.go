// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"

	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/catalog/catalogtest"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/events"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

const testMaintainer = "0193a4b2-6c1e-7a00-8000-000000000021"

// catalogFixture is the assembled server with the test kinds and rules, an administrator, a
// member (jdoe) and a maintainer who is in no product.
type catalogFixture struct {
	*apiFixture
	t *testing.T
}

func newCatalogAPI(t *testing.T) *catalogFixture {
	f := newAPIWith(t, fixtureOptions{setup: func(app *App) {
		app.catalogOptions.Kinds, app.catalogOptions.Rules = catalogtest.Kinds(), catalogtest.Rules()
		app.cfg.MaxBundleMB = 1
	}})
	ctx := context.Background()
	if _, err := f.app.db.ExecContext(ctx, `INSERT INTO users (id, username, display_name, status, is_admin, created_at, updated_at)
		VALUES (?, 'maint', 'Name of maint', 'active', FALSE, 1, 1)`, testMaintainer); err != nil {
		t.Fatal(err)
	}
	now := dbx.Millis(time.Now())
	if _, err := f.app.db.ExecContext(ctx, `INSERT INTO sessions (id, user_id, token_digest, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		dbx.NewID(), testMaintainer, authn.TokenDigest(testSession(testMaintainer)), now, now, now+dbx.Millis(time.Unix(0, 0).Add(authn.SessionMax))); err != nil {
		t.Fatal(err)
	}
	return &catalogFixture{apiFixture: f, t: t}
}

// call sends a request and checks the response against the contract. body is JSON unless
// contentType says otherwise.
func (f *catalogFixture) call(method, path, user string, body []byte, header map[string]string, want int) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	if user != "" {
		req.Header.Set(testUser, user)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != want {
		f.t.Fatalf("%s %s: %d, want %d\n%s", method, path, rec.Code, want, rec.Body)
	}
	vreq := req.Clone(context.Background())
	vreq.Body = io.NopCloser(bytes.NewReader(body))
	route, params, err := contract(f.t).FindRoute(vreq)
	if err != nil {
		f.t.Fatalf("%s %s is not in the contract: %v", method, path, err)
	}
	opts := &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, IncludeResponseStatus: true}
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: vreq, PathParams: params, Route: route, Options: opts},
		Status:                 rec.Code, Header: rec.Header(), Options: opts,
	}
	in.SetBodyBytes(rec.Body.Bytes())
	if err := openapi3filter.ValidateResponse(context.Background(), in); err != nil {
		f.t.Errorf("%s %s answered %d outside the contract: %v\n%s", method, path, rec.Code, err, rec.Body)
	}
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("%v\n%s", err, rec.Body)
	}
	return v
}

func jsonBody(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func skillZip(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range map[string]string{
		"unit-testing/SKILL.md":  "---\nname: unit-testing\ndescription: Writes unit tests.\n---\n" + body,
		"unit-testing/meta.yaml": "name: Unit testing\nwhen: When a change needs tests\nmaintainers: [maint]\n",
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(data))
	}
	zw.Close()
	return buf.Bytes()
}

func TestCatalogAPI(t *testing.T) {
	f := newCatalogAPI(t)
	octet := map[string]string{"Content-Type": "application/octet-stream"}
	upload := "/api/v1/resources/unit-testing/draft/bundle?kind=skill"

	f.call("PUT", upload, "", skillZip(t, "x\n"), octet, http.StatusUnauthorized)
	f.call("PUT", upload, testMember, skillZip(t, "x\n"), octet, http.StatusForbidden)
	f.call("PUT", upload, testAdmin, []byte("not an archive"), octet, http.StatusUnsupportedMediaType)
	rec := f.call("PUT", upload, testAdmin, skillZip(t, "Steps.\npassword=hunter2hunter2\n"), octet, http.StatusCreated)
	draft := decode[httpapi.Draft](t, rec)
	if rec.Header().Get("ETag") != draft.Etag || draft.Checks.Blocking != 1 || len(draft.Files) != 2 {
		t.Fatalf("upload: %s %s", rec.Header().Get("ETag"), rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "looks like a credential") {
		t.Errorf("the finding's message is not rendered: %s", rec.Body)
	}
	rec = f.call("GET", "/api/v1/resources/unit-testing/draft", testAdmin, nil, map[string]string{"Accept-Language": "zh-CN,zh;q=0.9"}, http.StatusOK)
	if strings.Contains(rec.Body.String(), "looks like a credential") {
		t.Errorf("not in Chinese: %s", rec.Body)
	}
	// A second upload needs the draft's ETag.
	f.call("PUT", upload, testAdmin, skillZip(t, "y\n"), octet, http.StatusPreconditionRequired)
	f.call("PUT", upload, testAdmin, skillZip(t, "y\n"), map[string]string{"Content-Type": "application/octet-stream", "If-Match": `"sha256:1"`}, http.StatusPreconditionFailed)

	// Publishing: If-Match first, then the must-fix finding.
	publish := "/api/v1/resources/unit-testing/publish"
	f.call("POST", publish, testAdmin, []byte(`{}`), nil, http.StatusPreconditionRequired)
	f.call("POST", publish, testAdmin, []byte(`{}`), map[string]string{"If-Match": `"sha256:1"`}, http.StatusPreconditionFailed)
	rec = f.call("POST", publish, testAdmin, []byte(`{}`), map[string]string{"If-Match": draft.Etag}, http.StatusConflict)
	if p := decode[httpapi.Problem](t, rec); p.Code != "resource.check_blocked" {
		t.Errorf("blocked: %+v", p)
	}
	finding := draft.Checks.Findings[0]
	for _, x := range draft.Checks.Findings {
		if x.Rule == "secrets" {
			finding = x
		}
	}
	f.call("POST", "/api/v1/resources/unit-testing/waivers", testMaintainer, jsonBody(map[string]string{"finding": finding.Id, "reason": "example"}), nil, http.StatusForbidden)
	f.call("POST", "/api/v1/resources/unit-testing/waivers", testAdmin, jsonBody(map[string]string{"finding": finding.Id, "reason": "A documented example"}), nil, http.StatusCreated)
	rec = f.call("POST", publish, testAdmin, jsonBody(map[string]string{"note": "First"}), map[string]string{"If-Match": draft.Etag}, http.StatusCreated)
	if v := decode[httpapi.ResourceVersion](t, rec); v.Number != 1 || v.Note == nil || *v.Note != "First" || v.PublishedBy.Username != "admin" {
		t.Errorf("published: %s", rec.Body)
	}

	// Management views.
	rec = f.call("GET", "/api/v1/resources", testAdmin, nil, nil, http.StatusOK)
	if l := decode[httpapi.ResourceList](t, rec); len(l.Items) != 1 || l.Items[0].Status != "published" || l.Items[0].HasDraft {
		t.Errorf("list: %s", rec.Body)
	}
	if l := decode[httpapi.ResourceList](t, f.call("GET", "/api/v1/resources", testMember, nil, nil, http.StatusOK)); len(l.Items) != 0 {
		t.Errorf("a member manages %+v", l.Items)
	}
	rec = f.call("GET", "/api/v1/resources/unit-testing", testMaintainer, nil, nil, http.StatusOK)
	res := decode[httpapi.ManagedResource](t, rec)
	if rec.Header().Get("ETag") != res.Etag || len(res.Maintainers) != 1 || res.Maintainers[0].Username != "maint" {
		t.Fatalf("resource: %s", rec.Body)
	}
	f.call("GET", "/api/v1/resources/unit-testing/draft", testAdmin, nil, nil, http.StatusNotFound)
	f.call("GET", "/api/v1/resources/unit-testing/versions", testAdmin, nil, nil, http.StatusOK)
	rec = f.call("GET", "/api/v1/resources/unit-testing/versions/1", testAdmin, nil, nil, http.StatusOK)
	if v := decode[httpapi.ResourceVersion](t, rec); v.Findings == nil || (*v.Findings)[0].Waiver == nil && (*v.Findings)[len(*v.Findings)-1].Waiver == nil {
		t.Errorf("version: %s", rec.Body)
	}

	// Settings change at once and become overrides.
	patch := "/api/v1/resources/unit-testing"
	f.call("PATCH", patch, testAdmin, jsonBody(map[string]string{"policy": "required"}), nil, http.StatusPreconditionRequired)
	f.call("PATCH", patch, testAdmin, jsonBody(map[string]string{"policy": "required"}), map[string]string{"If-Match": `"r-unit-testing-0"`}, http.StatusPreconditionFailed)
	f.call("PATCH", patch, testMaintainer, jsonBody(map[string]string{"policy": "required"}), map[string]string{"If-Match": res.Etag}, http.StatusForbidden)
	rec = f.call("PATCH", patch, testAdmin, jsonBody(map[string]string{"policy": "required"}), map[string]string{"If-Match": res.Etag}, http.StatusOK)
	res = decode[httpapi.ManagedResource](t, rec)
	if res.Policy != "required" || len(res.Overrides) != 1 || res.Overrides[0] != "policy" {
		t.Errorf("patched: %s", rec.Body)
	}

	// The published catalog.
	rec = f.call("GET", "/api/v1/catalog", testMember, nil, nil, http.StatusOK)
	if l := decode[httpapi.CatalogList](t, rec); len(l.Items) != 1 || !l.Items[0].Enabled || l.Items[0].Version != 1 {
		t.Errorf("catalog: %s", rec.Body)
	}
	rec = f.call("GET", "/api/v1/catalog/unit-testing", testMember, nil, nil, http.StatusOK)
	if r := decode[httpapi.CatalogResource](t, rec); len(r.Files) != 2 || r.Digest == "" {
		t.Errorf("catalog resource: %s", rec.Body)
	}
	rec = f.call("GET", "/api/v1/catalog/unit-testing/file?path=SKILL.md", testMember, nil, nil, http.StatusOK)
	if !strings.HasPrefix(rec.Body.String(), "---\nname: unit-testing") {
		t.Errorf("file: %q", rec.Body)
	}
	f.call("GET", "/api/v1/catalog/unit-testing/file?path=../etc/passwd", testMember, nil, nil, http.StatusBadRequest)

	// Disabling: impact first, then the change; the audit log has it for the maintainer.
	rec = f.call("GET", "/api/v1/resources/unit-testing/impact", testAdmin, nil, nil, http.StatusOK)
	if im := decode[httpapi.Impact](t, rec); im.People != 0 || len(im.ReferencedBy) != 0 {
		t.Errorf("impact: %s", rec.Body)
	}
	f.call("POST", "/api/v1/resources/unit-testing/disable", testAdmin, jsonBody(map[string]string{"reason": "Replaced"}), map[string]string{"If-Match": res.Etag}, http.StatusOK)
	if l := decode[httpapi.CatalogList](t, f.call("GET", "/api/v1/catalog", testMember, nil, nil, http.StatusOK)); len(l.Items) != 0 {
		t.Errorf("a disabled resource is in the catalog: %+v", l)
	}
	rec = f.call("GET", "/api/v1/audit?object_kind=resource&object_id=unit-testing", testMaintainer, nil, nil, http.StatusOK)
	page := decode[httpapi.AuditPage](t, rec)
	actions := map[string]bool{}
	for _, e := range page.Items {
		actions[e.Action] = true
		if e.Object.Id != "unit-testing" {
			t.Errorf("the maintainer sees another object: %+v", e)
		}
	}
	for _, a := range []string{"resource.create", "resource.publish", "resource.waive", "resource.configure", "resource.disable"} {
		if !actions[a] {
			t.Errorf("no %s in the resource's log: %v", a, actions)
		}
	}
	rec = f.call("GET", "/api/v1/audit?object_kind=resource&object_id=unit-testing", testMember, nil, nil, http.StatusOK)
	for _, e := range decode[httpapi.AuditPage](t, rec).Items {
		if e.Actor == nil || e.Actor.Id != testMember {
			t.Errorf("a member reads others' entries in the resource's log: %+v", e)
		}
	}

	rec = f.call("GET", "/api/v1/check-rules", testMember, nil, nil, http.StatusOK)
	if rs := decode[httpapi.RuleSet](t, rec); rs.Version != 1 || len(rs.Rules) != 2 {
		t.Errorf("rules: %s", rec.Body)
	}
}

func TestCatalogAPIUploadLimit(t *testing.T) {
	f := newCatalogAPI(t)
	big := bytes.Repeat([]byte("x"), 1<<20+1)
	upload := "/api/v1/resources/unit-testing/draft/bundle?kind=skill"
	// With a length: refused before anything is read.
	rec := f.call("PUT", upload, testAdmin, big, map[string]string{"Content-Type": "application/octet-stream"}, http.StatusRequestEntityTooLarge)
	if p := decode[httpapi.Problem](t, rec); p.Code != "request.too_large" {
		t.Errorf("problem: %+v", p)
	}
	// Without one: the read stops at the limit.
	req := httptest.NewRequest("PUT", upload, io.MultiReader(bytes.NewReader(big)))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(testUser, testAdmin)
	rr := httptest.NewRecorder()
	f.h.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("chunked: %d %s", rr.Code, rr.Body)
	}
}

func TestRegisterMCPOverHTTP(t *testing.T) {
	f := newCatalogAPI(t)
	put := "/api/v1/resources/jira/draft"
	body := func(header string) []byte {
		return jsonBody(map[string]any{"kind": "mcp", "meta": map[string]any{"name": "Jira", "when": "Issues"},
			"definition": map[string]any{"server": map[string]any{"type": "http", "url": "https://jira.example.com/mcp",
				"headers": map[string]any{"Authorization": header}}, "delivery": "native"}})
	}
	rec := f.call("PUT", put, testAdmin, body("password=hunter2hunter2"), nil, http.StatusBadRequest)
	if p := decode[httpapi.Problem](t, rec); p.Code != "resource.plaintext_secret" {
		t.Errorf("plain-text secret: %+v", p)
	}
	rec = f.call("PUT", put, testAdmin, body("Bearer ${secret:JIRA_TOKEN}"), nil, http.StatusCreated)
	d := decode[httpapi.Draft](t, rec)
	if d.Definition["delivery"] != "native" || d.Meta.Secrets == nil || (*d.Meta.Secrets)[0] != "JIRA_TOKEN" {
		t.Errorf("draft: %s", rec.Body)
	}
	f.call("PUT", put, testAdmin, body("Bearer ${secret:JIRA_TOKEN}"), map[string]string{"If-Match": d.Etag}, http.StatusOK)
	f.call("DELETE", put, testAdmin, nil, map[string]string{"If-Match": d.Etag}, http.StatusNoContent)
	f.call("GET", "/api/v1/resources/jira", testAdmin, nil, nil, http.StatusNotFound)
}

// TestCatalogEventsReachTheBus: the catalog publishes on the bus serve makes, once the change
// has committed.
func TestCatalogEventsReachTheBus(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{background: true, setup: func(app *App) {
		app.catalogOptions.Kinds, app.catalogOptions.Rules = catalogtest.Kinds(), catalogtest.Rules()
	}})
	var mu sync.Mutex
	var got []string
	f.app.bg.Events.Subscribe("test", nil, func(_ context.Context, ev events.Event) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev.Name)
		return nil
	})
	cf := &catalogFixture{apiFixture: f, t: t}
	cf.call("PUT", "/api/v1/resources/unit-testing/draft/bundle?kind=skill", testAdmin, skillZip(t, "Steps.\n"),
		map[string]string{"Content-Type": "application/octet-stream"}, http.StatusCreated)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.app.bg.Events.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(got, events.ResourceDraftUpdated{}.EventName()) {
		t.Errorf("events on the bus: %v", got)
	}
}

func TestListStages(t *testing.T) {
	f := newCatalogAPI(t)
	f.call("GET", "/api/v1/stages", "", nil, nil, http.StatusUnauthorized)

	// A stage a repository named, and a default one an administrator hid.
	ctx := context.Background()
	if _, err := f.app.db.ExecContext(ctx, `INSERT INTO stages (id, code, name, position, hidden, created_at, updated_at) VALUES (?, 'security', '', 100, FALSE, 1, 1), (?, 'scm', '', 5, TRUE, 1, 1)`,
		dbx.NewID(), dbx.NewID()); err != nil {
		t.Fatal(err)
	}
	type stage struct {
		ID, Name string
		Hidden   bool
	}
	list := func(lang string) []stage {
		t.Helper()
		rec := f.call("GET", "/api/v1/stages", testMember, nil, map[string]string{"Accept-Language": lang}, http.StatusOK)
		var out []stage
		for _, s := range decode[httpapi.StageList](t, rec).Items {
			out = append(out, stage{s.Id, s.Name, s.Hidden})
		}
		return out
	}
	want := []stage{
		{"requirement", "Requirements", false}, {"architecture", "Architecture", false}, {"design", "Design", false},
		{"develop", "Development", false}, {"test", "Testing", false}, {"scm", "SCM", true},
		{"release", "Release", false}, {"ops", "Operations", false}, {"security", "security", false},
	}
	if got := list("en"); !slices.Equal(got, want) {
		t.Errorf("en:\n got %v\nwant %v", got, want)
	}
	if got := list("zh-CN,zh;q=0.9"); len(got) != len(want) || got[0].Name != "需求" || got[5].Name != "配管" || got[8].Name != "security" {
		t.Errorf("zh-CN: %v", got)
	}
}
