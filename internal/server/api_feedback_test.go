// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

// publishSkill uploads and publishes unit-testing (maintained by maint) as the administrator.
func (f *catalogFixture) publishSkill() {
	f.t.Helper()
	rec := f.call("PUT", "/api/v1/resources/unit-testing/draft/bundle?kind=skill", testAdmin, skillZip(f.t, "Steps.\n"),
		map[string]string{"Content-Type": "application/octet-stream"}, http.StatusCreated)
	d := decode[httpapi.Draft](f.t, rec)
	f.call("POST", "/api/v1/resources/unit-testing/publish", testAdmin, []byte(`{}`), map[string]string{"If-Match": d.Etag}, http.StatusCreated)
}

func TestFeedbackAPI(t *testing.T) {
	f := newCatalogAPI(t)
	f.publishSkill()

	// The context keeps the agreed fields of the console and nothing else: an extra key in
	// the request is not stored anywhere.
	body := []byte(`{"type":"bug","title":"var in a Java 8 project","body":"The tests did not compile.","resource":"unit-testing",
		"resource_version":1,"context":{"page":"/catalog/unit-testing?x=1","browser":"Firefox 140","tool":"claude-code","os":"darwin",
		"conversation":"the whole chat <placeholder>"}}`)
	f.call("POST", "/api/v1/feedback", "", body, nil, http.StatusUnauthorized)
	rec := f.call("POST", "/api/v1/feedback", testMember, body, nil, http.StatusCreated)
	fb := decode[httpapi.Feedback](t, rec)
	if fb.Origin != httpapi.FeedbackOriginWeb || fb.Status != "new" || fb.Submitter == nil || fb.Submitter.Username != "jdoe" ||
		fb.Resource == nil || fb.Resource.Code != "unit-testing" || len(fb.Events) != 1 {
		t.Fatalf("created: %s", rec.Body)
	}
	if c := fb.Context; c.Page == nil || *c.Page != "/catalog/unit-testing" || c.Browser == nil || c.Tool != nil || c.Os != nil {
		t.Errorf("context: %s", rec.Body)
	}
	var stored string
	if err := f.app.db.QueryRowContext(context.Background(), `SELECT context FROM feedback WHERE number = ?`, fb.Number).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "conversation") || strings.Contains(stored, "placeholder") {
		t.Errorf("stored context: %s", stored)
	}
	path := "/api/v1/feedback/" + strconv.Itoa(fb.Number)

	// From the command line (the administrator's device), drafted by an AI tool.
	rec = f.call("POST", "/api/v1/feedback", "", []byte(`{"type":"question","title":"How do I run it?","body":"Where are the steps?",
		"ai_drafted":true,"context":{"tool":"claude-code","cli_version":"0.5.0","os":"linux","last_sync":"partial","page":"/x",
		"frictions":[{"type":"tool_error","count":3}]}}`), map[string]string{"Authorization": "Bearer " + f.deviceToken}, http.StatusCreated)
	cli := decode[httpapi.Feedback](t, rec)
	if cli.Origin != httpapi.FeedbackOriginAi || cli.Context.Page != nil || cli.Context.LastSync == nil || cli.Context.Frictions == nil {
		t.Errorf("from the command line: %s", rec.Body)
	}
	f.call("POST", "/api/v1/feedback", "", []byte(`{"type":"bug","title":"x","body":"y","context":{"tool":"Claude Code"}}`),
		map[string]string{"Authorization": "Bearer " + f.deviceToken}, http.StatusBadRequest)

	// The maintainer handles it; the submitter does not.
	f.call("PATCH", path, testMember, jsonBody(map[string]string{"status": "accepted"}), nil, http.StatusForbidden)
	rec = f.call("PATCH", path, testMaintainer, jsonBody(map[string]string{"status": "wont_fix"}), nil, http.StatusBadRequest)
	if p := decode[httpapi.Problem](t, rec); p.Code != "feedback.reason_required" {
		t.Errorf("wont_fix without a reason: %s", rec.Body)
	}
	rec = f.call("PATCH", path, testMaintainer, jsonBody(map[string]string{"status": "resolved"}), nil, http.StatusOK)
	if got := decode[httpapi.Feedback](t, rec); got.Status != "resolved" || got.Assignee == nil || got.Assignee.Username != "maint" {
		t.Errorf("resolved: %s", rec.Body)
	}

	// A low score reopens it.
	f.call("POST", path+"/rate", testMaintainer, jsonBody(map[string]int{"score": 5}), nil, http.StatusForbidden)
	rec = f.call("POST", path+"/rate", testMember, jsonBody(map[string]any{"score": 1, "note": "Same error"}), nil, http.StatusOK)
	if got := decode[httpapi.Feedback](t, rec); got.Status != "accepted" || got.Score == nil || *got.Score != 1 {
		t.Errorf("after a low score: %s", rec.Body)
	}
	rec = f.call("POST", path+"/rate", testMember, jsonBody(map[string]int{"score": 4}), nil, http.StatusConflict)
	if p := decode[httpapi.Problem](t, rec); p.Code != "feedback.not_resolved" {
		t.Errorf("rate open feedback: %s", rec.Body)
	}

	rec = f.call("POST", path+"/comments", testMember, jsonBody(map[string]string{"body": "Still there on v1."}), nil, http.StatusCreated)
	if e := decode[httpapi.FeedbackEvent](t, rec); e.Kind != httpapi.Comment || e.By == nil || e.By.Username != "jdoe" {
		t.Errorf("comment: %s", rec.Body)
	}
	rec = f.call("GET", path, testMaintainer, nil, nil, http.StatusOK)
	if got := decode[httpapi.Feedback](t, rec); len(got.Events) != 6 {
		t.Errorf("timeline: %s", rec.Body)
	}

	rec = f.call("GET", "/api/v1/feedback?mine=true", testMember, nil, nil, http.StatusOK)
	if page := decode[httpapi.FeedbackPage](t, rec); len(page.Items) != 1 || page.Items[0].Number != fb.Number {
		t.Errorf("mine: %s", rec.Body)
	}
	rec = f.call("GET", "/api/v1/feedback?limit=1", testAdmin, nil, nil, http.StatusOK)
	page := decode[httpapi.FeedbackPage](t, rec)
	if len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatalf("first page: %s", rec.Body)
	}
	f.call("GET", "/api/v1/feedback?limit=1&cursor="+*page.NextCursor, testAdmin, nil, nil, http.StatusOK)
	f.call("GET", "/api/v1/feedback/999", testAdmin, nil, nil, http.StatusNotFound)
	rec = f.call("GET", "/api/v1/feedback/summary", testMaintainer, nil, nil, http.StatusOK)
	if s := decode[httpapi.FeedbackSummary](t, rec); s.Open != 1 || s.ByStatus.Accepted != 1 {
		t.Errorf("summary: %s", rec.Body)
	}
}

// TestFeedbackNotifications: through the bus and notify, resolving tells the submitter, and the
// submitter's low score tells the handler; nobody hears of their own action.
func TestFeedbackNotifications(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{background: true})
	cf := &catalogFixture{apiFixture: f, t: t}
	rec := cf.call("POST", "/api/v1/feedback", testMember, []byte(`{"type":"bug","title":"x","body":"y"}`), nil, http.StatusCreated)
	path := "/api/v1/feedback/" + strconv.Itoa(decode[httpapi.Feedback](t, rec).Number)
	cf.call("PATCH", path, testAdmin, jsonBody(map[string]string{"status": "resolved"}), nil, http.StatusOK)
	drain(t, f)
	kinds := func(user string) []string {
		var out []string
		for _, it := range myNotifications(t, f, user, "").Items {
			out = append(out, it.Kind)
		}
		return out
	}
	if got := kinds(testMember); len(got) != 1 || got[0] != "feedback.status_changed" {
		t.Errorf("the submitter after resolving: %v", got)
	}
	if got := kinds(testAdmin); len(got) != 0 {
		t.Errorf("the handler heard of their own change: %v", got)
	}
	cf.call("POST", path+"/rate", testMember, jsonBody(map[string]int{"score": 2}), nil, http.StatusOK)
	drain(t, f)
	if got := kinds(testAdmin); len(got) != 1 || got[0] != "feedback.status_changed" {
		t.Errorf("the handler after a low score: %v", got)
	}
	if got := kinds(testMember); len(got) != 1 {
		t.Errorf("the submitter heard of their own score: %v", got)
	}
}
