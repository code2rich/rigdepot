// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authnapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/server/access"
)

// The middleware reads each operation's security from the contract built
// into the program; these are the cases it has to tell apart.
func TestSecurityFromTheContract(t *testing.T) {
	ops, err := contract()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		method, path string
		want         security
	}{
		{"POST", "/api/v1/auth/login", security{id: "login", public: true, optional: true}},
		{"GET", "/api/v1/auth/invites/abcdefghijklmnopq", security{id: "getInvite", public: true, optional: true}},
		{"POST", "/api/v1/auth/logout", security{id: "logout", session: true}},
		{"GET", "/api/v1/me", security{id: "getMe", session: true, device: true}},
		{"POST", "/api/v1/me/tokens", security{id: "createAccessToken", session: true}},
		{"POST", "/api/v1/me/password", security{id: "changeMyPassword", session: true}},
		{"GET", "/api/v1/stages", security{id: "listStages", session: true, device: true, token: true, scope: access.TokenRead}},
		{"GET", "/api/v1/resources", security{id: "listResources", session: true, device: true, token: true, scope: access.TokenPublish}},
	} {
		_, got, ok := ops.find(httptest.NewRequest(c.method, c.path, nil))
		if !ok || got != c.want {
			t.Errorf("%s %s: %+v (%v), want %+v", c.method, c.path, got, ok, c.want)
		}
	}
	if _, _, ok := ops.find(httptest.NewRequest("GET", "/api/v1/no-such-thing", nil)); ok {
		t.Error("a path outside the contract is an operation")
	}
	if len(ops.ops) < 100 {
		t.Errorf("only %d operations", len(ops.ops))
	}
}

func TestSecurityRefusesWhatItDoesNotKnow(t *testing.T) {
	for _, spec := range []string{
		"paths:\n  /a:\n    get:\n      operationId: a\n      security: [{ apiKey: [] }]\n",
		"paths:\n  /a:\n    get:\n      operationId: a\n      security: [{ accessToken: [] }]\n",
		"paths:\n  /a:\n    get:\n      operationId: a\n",
	} {
		if _, err := parseSecurity([]byte(spec)); err == nil {
			t.Errorf("accepted:\n%s", spec)
		} else if !strings.Contains(err.Error(), " a") {
			t.Errorf("the error does not name the operation: %v", err)
		}
	}
}

func TestCovers(t *testing.T) {
	for _, c := range []struct {
		have, need access.TokenScope
		want       bool
	}{
		{access.TokenRead, access.TokenRead, true},
		{access.TokenRead, access.TokenPublish, false},
		{access.TokenPublish, access.TokenRead, true},
		{access.TokenPublish, access.TokenPublish, true},
		{"", access.TokenRead, false},
		{"admin", access.TokenRead, false},
	} {
		if got := covers(c.have, c.need); got != c.want {
			t.Errorf("covers(%q, %q) = %v", c.have, c.need, got)
		}
	}
}
