// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"

	contractspec "github.com/code2rich/rigdepot/api"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

var (
	contractOnce   sync.Once
	contractRouter routers.Router
	contractErr    error
)

// contract returns a router over api/openapi.yaml, loaded once.
func contract(t *testing.T) routers.Router {
	t.Helper()
	contractOnce.Do(func() {
		doc, err := openapi3.NewLoader().LoadFromData(contractspec.Spec)
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

// call is do for an operation of the contract: it fails the test when the status is not want
// or the response is not one the contract allows for the operation.
func (f *apiFixture) call(t *testing.T, method, path, user, body string, header map[string]string, want int) (*httptest.ResponseRecorder, httpapi.Problem) {
	t.Helper()
	rec, p := f.do(t, method, path, user, body, header)
	if rec.Code != want {
		t.Fatalf("%s %s as %q: %d, want %d\n%s", method, path, user, rec.Code, want, rec.Body)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	route, params, err := contract(t).FindRoute(req)
	if err != nil {
		t.Fatalf("%s %s is not in the contract: %v", method, path, err)
	}
	req.Body = io.NopCloser(bytes.NewReader([]byte(body)))
	opts := &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, IncludeResponseStatus: true}
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route, Options: opts},
		Status:                 rec.Code,
		Header:                 rec.Header(),
		Options:                opts,
	}
	in.SetBodyBytes(rec.Body.Bytes())
	if err := openapi3filter.ValidateResponse(context.Background(), in); err != nil {
		t.Errorf("%s %s answered %d outside the contract: %v\n%s", method, path, rec.Code, err, rec.Body)
	}
	return rec, p
}
