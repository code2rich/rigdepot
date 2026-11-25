// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package oidc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/identity"
	"github.com/code2rich/rigdepot/ext/identity/identitytest"
)

func TestMain(m *testing.M) { identitytest.Main(m) }

func TestContract(t *testing.T) { identitytest.Run(t, New()) }

func settings(o *identitytest.OIDC) map[string]any {
	return map[string]any{"issuer": o.URL, "client_id": o.ClientID}
}

func TestSignIn(t *testing.T) { identitytest.RunOIDC(t, New(), settings) }

func TestValidate(t *testing.T) {
	for issuer, want := range map[string]string{
		"https://sso.example.com/realms/dev": "",
		"http://127.0.0.1:8080":              "",
		"http://localhost:8080/":             "",
		"http://sso.example.com":             "https",
		"sso.example.com":                    "format",
		"https://sso.example.com/?x=1":       "format",
		"https://user@sso.example.com":       "format",
		"":                                   "required",
	} {
		err := New().Validate(identity.Config{Settings: map[string]any{"issuer": issuer, "client_id": "c"}})
		var ce *identity.ConfigError
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: %v", issuer, err)
		case want != "" && (!errors.As(err, &ce) || ce.Field != "issuer" || ce.Code != want):
			t.Errorf("%q: %v, want issuer %s", issuer, err, want)
		}
	}
}

// TestExtraScopes: the scopes setting adds to openid, profile and email.
func TestExtraScopes(t *testing.T) {
	o := identitytest.NewOIDC(t, "")
	cfg := identity.Config{Settings: map[string]any{"issuer": o.URL, "client_id": o.ClientID, "scopes": []any{"groups"}}, ClientSecret: o.ClientSecret}
	authURL, _, err := New().Begin(context.Background(), cfg, "http://127.0.0.1:8080/callback")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(authURL, "scope=openid+profile+email+groups") {
		t.Errorf("authorization request %s", authURL)
	}
}

// TestGroupsAndUsername: the name the person has at the provider and their groups come along.
func TestGroupsAndUsername(t *testing.T) {
	o := identitytest.NewOIDC(t, "")
	o.SignIn(map[string]any{"sub": "s", "preferred_username": "jdoe", "groups": []any{"dev", "ops"}, "email_verified": "true", "email": "jdoe@example.com"})
	p := New()
	cfg := identity.Config{Settings: settings(o), ClientSecret: o.ClientSecret}
	authURL, state, err := p.Begin(context.Background(), cfg, "http://127.0.0.1:8080/callback")
	if err != nil {
		t.Fatal(err)
	}
	id, err := p.Complete(context.Background(), cfg, state, o.Authorize(t, authURL))
	if err != nil {
		t.Fatal(err)
	}
	if id.Username != "jdoe" || strings.Join(id.Groups, ",") != "dev,ops" || !id.EmailVerified {
		t.Errorf("identity = %+v", id)
	}
}
