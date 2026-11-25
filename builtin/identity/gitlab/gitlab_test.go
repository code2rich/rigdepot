// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package gitlab

import (
	"context"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/identity"
	"github.com/code2rich/rigdepot/ext/identity/identitytest"
)

func TestMain(m *testing.M) { identitytest.Main(m) }

func TestContract(t *testing.T) { identitytest.Run(t, New()) }

// TestSignIn: the GitLab address with a trailing slash, as people paste it, is the issuer.
func TestSignIn(t *testing.T) {
	identitytest.RunOIDC(t, New(), func(o *identitytest.OIDC) map[string]any {
		return map[string]any{"url": o.URL + "/", "client_id": o.ClientID}
	})
}

// TestClaims: GitLab's ID token names the account in nickname and the groups in groups_direct
// (claims as GitLab 18 sends them, values replaced).
func TestClaims(t *testing.T) {
	o := identitytest.NewOIDC(t, "/gitlab")
	o.SignIn(map[string]any{
		"sub": "42", "sub_legacy": "<placeholder>", "name": "Jane Doe", "nickname": "jdoe", "preferred_username": "jdoe-preferred",
		"email": "jdoe@example.com", "email_verified": true, "profile": "https://gitlab.example.com/jdoe",
		"picture": "https://gitlab.example.com/uploads/avatar.png", "groups_direct": []any{"platform", "platform/web"},
	})
	p := New()
	cfg := identity.Config{Settings: map[string]any{"url": o.URL, "client_id": o.ClientID}, ClientSecret: o.ClientSecret}
	authURL, state, err := p.Begin(context.Background(), cfg, "http://127.0.0.1:8080/callback")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(authURL, "scope=openid+profile+email") {
		t.Errorf("authorization request %s", authURL)
	}
	id, err := p.Complete(context.Background(), cfg, state, o.Authorize(t, authURL))
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "42" || id.Username != "jdoe" || id.DisplayName != "Jane Doe" || !id.EmailVerified || strings.Join(id.Groups, ",") != "platform,platform/web" {
		t.Errorf("identity = %+v", id)
	}
}
