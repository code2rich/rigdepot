// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/identity"
)

func TestReturnPath(t *testing.T) {
	for in, want := range map[string]string{
		"/catalog":                 "/catalog",
		"/resources/x?tab=2#v":     "/resources/x?tab=2#v",
		"":                         "",
		"catalog":                  "",
		"//evil.example.com/x":     "",
		`/\evil.example.com`:       "",
		"https://evil.example.com": "",
		"/a\r\nSet-Cookie: x":      "",
	} {
		if got := ReturnPath(in); got != want {
			t.Errorf("ReturnPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUsernameCandidates(t *testing.T) {
	for _, tc := range []struct {
		id    identity.Identity
		email string
		first string
	}{
		{identity.Identity{Username: "Jane.Doe"}, "", "jane.doe"},
		{identity.Identity{Username: "张三"}, "zhang.san@example.com", "zhang.san"},
		{identity.Identity{}, "", "user"},
		{identity.Identity{Username: "a b/c"}, "", "a-b-c"},
		{identity.Identity{Username: strings.Repeat("x", 40)}, "", strings.Repeat("x", 28)},
	} {
		got := usernameCandidates(tc.id, tc.email)
		if got[0] != tc.first || len(got) != maxNameTries || got[1] != tc.first+"-2" {
			t.Errorf("usernameCandidates(%+v, %q) = %v", tc.id, tc.email, got[:3])
		}
	}
}

// TestMaskHidesEveryForm: the client secret as it is, URL-encoded, in base64, and inside the
// HTTP basic credentials of a token request.
func TestMaskHidesEveryForm(t *testing.T) {
	secret := "s3cr3t/+value=="
	cfg := identity.Config{Settings: map[string]any{"client_id": "app id"}, ClientSecret: secret}
	basic := base64.StdEncoding.EncodeToString([]byte(url.QueryEscape("app id") + ":" + url.QueryEscape(secret)))
	text := strings.Join([]string{secret, url.QueryEscape(secret), base64.StdEncoding.EncodeToString([]byte(secret)), "Basic " + basic}, " | ")
	got := mask(text, cfg)
	for _, form := range []string{secret, url.QueryEscape(secret), base64.StdEncoding.EncodeToString([]byte(secret)), basic} {
		if strings.Contains(got, form) {
			t.Errorf("mask left %q in %q", form, got)
		}
	}
	if strings.Count(got, "<client secret>") != 4 {
		t.Errorf("mask = %q", got)
	}
}
