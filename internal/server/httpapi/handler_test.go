// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// stub implements the generated interface; every operation it does not
// override panics, which a test would notice.
type stub struct {
	StrictServerInterface
	listFeedback func(ListFeedbackRequestObject) (ListFeedbackResponseObject, error)
}

func (s stub) ListFeedback(_ context.Context, req ListFeedbackRequestObject) (ListFeedbackResponseObject, error) {
	return s.listFeedback(req)
}

func serve(t *testing.T, impl StrictServerInterface, method, target, body string, header http.Header) (*httptest.ResponseRecorder, Problem) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	NewHandler(impl, http.NewServeMux()).ServeHTTP(rec, req)
	var p Problem
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/problem+json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("problem document does not decode: %v\n%s", err, rec.Body)
		}
	}
	return rec, p
}

func TestMissingIfMatchIsPreconditionRequired(t *testing.T) {
	rec, p := serve(t, stub{}, http.MethodPatch, "/api/v1/settings", `{}`, nil)
	if rec.Code != http.StatusPreconditionRequired || p.Code != CodePreconditionRequired {
		t.Errorf("PATCH /api/v1/settings without If-Match: %d %q, want 428 %q", rec.Code, p.Code, CodePreconditionRequired)
	}
}

func TestBadParameterNamesTheField(t *testing.T) {
	rec, p := serve(t, stub{}, http.MethodGet, "/api/v1/feedback?limit=many", "", nil)
	if rec.Code != http.StatusBadRequest || p.Code != CodeRequestInvalid {
		t.Fatalf("limit=many: %d %q, want 400 %q", rec.Code, p.Code, CodeRequestInvalid)
	}
	if p.Errors == nil || len(*p.Errors) != 1 || (*p.Errors)[0].Field != "limit" {
		t.Errorf("errors = %+v, want one entry for limit", p.Errors)
	}
}

func TestUndecodableBodyIsInvalid(t *testing.T) {
	rec, p := serve(t, stub{}, http.MethodPatch, "/api/v1/settings", `{"organization":`, http.Header{"If-Match": {`"settings-1"`}})
	if rec.Code != http.StatusBadRequest || p.Code != CodeRequestInvalid {
		t.Errorf("truncated body: %d %q, want 400 %q", rec.Code, p.Code, CodeRequestInvalid)
	}
}

func TestImplementationErrorIsServerError(t *testing.T) {
	impl := stub{listFeedback: func(ListFeedbackRequestObject) (ListFeedbackResponseObject, error) {
		return nil, errors.New("database is locked")
	}}
	rec, p := serve(t, impl, http.MethodGet, "/api/v1/feedback", "", nil)
	if rec.Code != http.StatusInternalServerError || p.Code != CodeServerError {
		t.Errorf("failing implementation: %d %q, want 500 %q", rec.Code, p.Code, CodeServerError)
	}
	if strings.Contains(rec.Body.String(), "database is locked") {
		t.Error("the internal error leaked into the response")
	}
}

func TestResponsesFollowTheContract(t *testing.T) {
	next := "eyJiIjoxfQ"
	impl := stub{listFeedback: func(req ListFeedbackRequestObject) (ListFeedbackResponseObject, error) {
		if req.Params.Mine == nil || !*req.Params.Mine {
			t.Errorf("mine=true did not arrive: %+v", req.Params)
		}
		return ListFeedback200JSONResponse{Items: []FeedbackListItem{}, NextCursor: &next}, nil
	}}
	rec, _ := serve(t, impl, http.MethodGet, "/api/v1/feedback?mine=true", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var page map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if _, ok := page["items"]; !ok || page["next_cursor"] != next {
		t.Errorf("page = %v, want items and next_cursor", page)
	}
}
