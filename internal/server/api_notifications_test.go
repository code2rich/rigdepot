// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	extnotify "github.com/code2rich/rigdepot/ext/notify"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/catalog/catalogtest"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/jobs"
)

const testApplicant = "0193a4b2-6c1e-7a00-8000-000000000031"

// drain waits until the event handlers have handled everything published so far.
func drain(t *testing.T, f *apiFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.app.bg.Events.Drain(ctx); err != nil {
		t.Fatal(err)
	}
}

func myNotifications(t *testing.T, f *apiFixture, user, query string) httpapi.NotificationPage {
	t.Helper()
	rec, _ := f.call(t, http.MethodGet, "/api/v1/me/notifications"+query, user, "", nil, http.StatusOK)
	var page httpapi.NotificationPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

// TestNotificationsOverHTTP: access's events reach the right people through the bus and the
// rules; each reads their own, pages through them and marks them read.
func TestNotificationsOverHTTP(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{background: true})
	ctx := context.Background()
	if _, err := f.app.db.ExecContext(ctx, `INSERT INTO users (id, username, display_name, status, is_admin, created_at, updated_at)
		VALUES (?, 'applicant', 'Applicant', 'pending', FALSE, 1, 1)`, testApplicant); err != nil {
		t.Fatal(err)
	}
	admin := access.Actor{UserID: testAdmin, Origin: access.OriginWeb}
	if _, err := f.app.access.CreateProduct(ctx, admin, access.NewProduct{Code: "shop", Name: "Shop", Owners: []string{testMember}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.access.RequestToJoin(ctx, access.Actor{UserID: testApplicant, Origin: access.OriginWeb}, []string{"shop"}, ""); err != nil {
		t.Fatal(err)
	}
	drain(t, f)

	f.call(t, http.MethodGet, "/api/v1/me/notifications", "", "", nil, http.StatusUnauthorized)
	page := myNotifications(t, f, testMember, "")
	if len(page.Items) != 2 || page.Unread != 2 || page.Items[0].Kind != "access.join_requested" || page.Items[0].Title != "Applicant asked to join Shop" ||
		page.Items[1].Kind != "access.membership_changed" || page.Items[0].Link == nil || *page.Items[0].Link != "/products" {
		t.Fatalf("the owner's notifications: %+v", page)
	}
	if admin := myNotifications(t, f, testAdmin, ""); len(admin.Items) != 0 || admin.Unread != 0 {
		t.Errorf("whoever created the product heard of it: %+v", admin)
	}

	first := myNotifications(t, f, testMember, "?limit=1")
	if len(first.Items) != 1 || first.NextCursor == nil || first.Items[0].Id != page.Items[0].Id {
		t.Fatalf("first page: %+v", first)
	}
	second := myNotifications(t, f, testMember, "?limit=1&cursor="+*first.NextCursor)
	if len(second.Items) != 1 || second.NextCursor != nil || second.Items[0].Id != page.Items[1].Id {
		t.Errorf("second page: %+v", second)
	}

	f.call(t, http.MethodPost, "/api/v1/me/notifications/mark-read", testAdmin, `{"ids":["`+page.Items[0].Id+`"]}`, nil, http.StatusNoContent)
	if p := myNotifications(t, f, testMember, ""); p.Unread != 2 {
		t.Errorf("someone else marked the owner's notification read: %+v", p)
	}
	f.call(t, http.MethodPost, "/api/v1/me/notifications/mark-read", testMember, `{"ids":["`+page.Items[0].Id+`"]}`, nil, http.StatusNoContent)
	unread := myNotifications(t, f, testMember, "?unread=true")
	if len(unread.Items) != 1 || unread.Unread != 1 || unread.Items[0].Id != page.Items[1].Id {
		t.Errorf("after marking one read: %+v", unread)
	}
	f.call(t, http.MethodPost, "/api/v1/me/notifications/mark-read", testMember, `{"all":true}`, nil, http.StatusNoContent)
	if p := myNotifications(t, f, testMember, ""); p.Unread != 0 || p.Items[0].ReadAt == nil {
		t.Errorf("after marking all read: %+v", p)
	}

	for _, body := range []string{`{}`, `{"all":false}`, `{"all":true,"ids":[]}`} {
		if _, p := f.call(t, http.MethodPost, "/api/v1/me/notifications/mark-read", testMember, body, nil, http.StatusBadRequest); p.Code != httpapi.CodeRequestInvalid {
			t.Errorf("%s: %+v", body, p)
		}
	}
	if _, p := f.call(t, http.MethodGet, "/api/v1/me/notifications?cursor=not-a-cursor", testMember, "", nil, http.StatusBadRequest); p.Code != httpapi.CodeRequestInvalid {
		t.Errorf("bad cursor: %+v", p)
	}
}

// TestNotificationsWithoutBackgroundAre501: without the background parts there is nothing that
// writes notifications.
func TestNotificationsWithoutBackgroundAre501(t *testing.T) {
	f := newAPI(t, false)
	f.call(t, http.MethodGet, "/api/v1/me/notifications", testMember, "", nil, http.StatusNotImplemented)
}

// TestBrokenAuditChainNotifiesAdministrators: the daily check that finds the chain broken tells
// the administrators in the app.
func TestBrokenAuditChainNotifiesAdministrators(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{background: true})
	ctx := context.Background()
	// The scheduled check runs once at start. Let it finish first: run between the product's
	// entry and the tampering, it would mark that entry as checked, and the check below would
	// not look at it again.
	deadline := time.Now().Add(15 * time.Second)
	for {
		sts, err := f.app.bg.Jobs.Statuses(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if slices.ContainsFunc(sts, func(s jobs.Status) bool { return s.Name == auditCheckJob && s.LastResult != "" }) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the audit check did not run at start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	admin := access.Actor{UserID: testAdmin, Origin: access.OriginWeb}
	if _, err := f.app.access.CreateProduct(ctx, admin, access.NewProduct{Code: "shop", Name: "Shop"}); err != nil {
		t.Fatal(err)
	}
	tamper(t, f, `UPDATE audit_log SET object_label = 'renamed' WHERE seq = (SELECT MAX(seq) FROM audit_log)`)
	if err := f.app.checkAudit(ctx); err == nil {
		t.Fatal("the check passed")
	}
	drain(t, f)
	// The scheduled run of the check may find the break too: one notification per run.
	page := myNotifications(t, f, testAdmin, "")
	if len(page.Items) == 0 {
		t.Fatal("the administrator was not notified")
	}
	for _, it := range page.Items {
		if it.Kind != "audit.chain_broken" || !strings.Contains(it.Title, "tampered") || it.Link == nil || *it.Link != "/admin/audit" {
			t.Errorf("the administrator's notification: %+v", it)
		}
	}
	if p := myNotifications(t, f, testMember, ""); len(p.Items) != 0 {
		t.Errorf("a member heard of it: %+v", p)
	}
}

// TestInvitationsAreMailed: once mail is set up the invitation goes out by mail and the link is
// not shown; before, the administrator gets the link to pass on.
func TestInvitationsAreMailed(t *testing.T) {
	ch := &fakeMail{}
	f := newAPIWith(t, fixtureOptions{background: true, channels: ch.lookup})
	var out httpapi.InviteList
	rec, _ := f.call(t, http.MethodPost, "/api/v1/invites", testAdmin, `{"emails":["first@example.com"]}`, nil, http.StatusCreated)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Items) != 1 || out.Items[0].Link == nil {
		t.Fatalf("without mail the link is shown: %s %v", rec.Body, err)
	}

	rec, _ = f.do(t, http.MethodGet, "/api/v1/settings", testAdmin, "", nil)
	change := `{"mail":{"enabled":true,"host":"smtp.example.com","port":465,"security":"tls","from":"rigdepot@example.com"}}`
	if rec, _ = f.do(t, http.MethodPatch, "/api/v1/settings", testAdmin, change, map[string]string{"If-Match": rec.Header().Get("ETag")}); rec.Code != http.StatusOK {
		t.Fatalf("mail settings: %d %s", rec.Code, rec.Body)
	}
	rec, _ = f.call(t, http.MethodPost, "/api/v1/invites", testAdmin, `{"emails":["second@example.com"]}`, nil, http.StatusCreated)
	out = httpapi.InviteList{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Items) != 1 || out.Items[0].Link != nil {
		t.Fatalf("with mail the link is not shown: %s %v", rec.Body, err)
	}
	var got []extnotify.Message
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		ch.mu.Lock()
		got = append([]extnotify.Message(nil), ch.msgs...)
		ch.mu.Unlock()
		if len(got) > 0 {
			break
		}
	}
	if len(got) != 1 || got[0].To.Address != "second@example.com" || !strings.HasPrefix(got[0].Link, "http://127.0.0.1:8080/invite/") ||
		!strings.Contains(got[0].Text, got[0].Link) {
		t.Errorf("mailed %+v", got)
	}
}

// TestCatalogNotifications: a draft and its publication reach the resource's maintainer through
// the catalog's events; the administrator who uploaded and published hears of neither.
func TestCatalogNotifications(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{background: true, setup: func(app *App) {
		app.catalogOptions.Kinds, app.catalogOptions.Rules = catalogtest.Kinds(), catalogtest.Rules()
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
	cf := &catalogFixture{apiFixture: f, t: t}
	rec := cf.call("PUT", "/api/v1/resources/unit-testing/draft/bundle?kind=skill", testAdmin, skillZip(t, "Steps.\n"),
		map[string]string{"Content-Type": "application/octet-stream"}, http.StatusCreated)
	cf.call("POST", "/api/v1/resources/unit-testing/publish", testAdmin, []byte(`{}`), map[string]string{"If-Match": rec.Header().Get("ETag")}, http.StatusCreated)
	drain(t, f)

	page := myNotifications(t, f, testMaintainer, "")
	var kinds, titles []string
	for _, it := range page.Items {
		kinds, titles = append(kinds, it.Kind), append(titles, it.Title)
	}
	if !slices.Equal(kinds, []string{"catalog.published", "catalog.draft_updated"}) ||
		titles[0] != "Unit testing v1 is published" || titles[1] != "New resource Unit testing is waiting to be published" {
		t.Errorf("the maintainer's notifications: %v %v", kinds, titles)
	}
	if p := myNotifications(t, f, testAdmin, ""); len(p.Items) != 0 {
		t.Errorf("the administrator who acted was told: %+v", p.Items)
	}
}
