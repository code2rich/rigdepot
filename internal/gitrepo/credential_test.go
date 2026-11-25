// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package gitrepo_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/gitrepo"
)

// TestCredentialIsSentAsBasicAuth: a credential reaches an https server as basic authentication,
// with the address's user name or the default one, and never shows in the error.
func TestCredentialIsSentAsBasicAuth(t *testing.T) {
	const token = "<placeholder-token>-0123456789"
	var mu sync.Mutex
	var got []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get("Authorization"))
		mu.Unlock()
		http.Error(w, "repository not found", http.StatusNotFound)
	}))
	defer srv.Close()
	t.Setenv("GIT_SSL_NO_VERIFY", "1") // the test server's certificate is self-signed

	f := newFetcher(t, gitrepo.Options{Timeout: 30 * time.Second})
	basic := func(user string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+token))
	}
	for _, c := range []struct {
		url, user, want string
	}{
		{srv.URL + "/example/resources.git", "", basic("oauth2")},
		{strings.Replace(srv.URL, "https://", "https://deploy-user@", 1) + "/example/resources.git", "", basic("deploy-user")},
		{srv.URL + "/example/resources.git", "someone", basic("someone")},
	} {
		got = nil
		_, err := f.Snapshot(context.Background(), gitrepo.Source{URL: c.url, Credential: &gitrepo.Credential{Username: c.user, Password: token}})
		if !errors.Is(err, gitrepo.ErrFetch) {
			t.Fatalf("%s: err = %v, want ErrFetch", c.url, err)
		}
		if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), base64.StdEncoding.EncodeToString([]byte(token))[:8]) {
			t.Errorf("the error shows the credential: %v", err)
		}
		mu.Lock()
		if len(got) == 0 || got[0] != c.want {
			t.Errorf("%s: Authorization = %q, want %q", c.url, got, c.want)
		}
		mu.Unlock()
	}
}

func TestCredentialOnlyForHTTPS(t *testing.T) {
	r := newRepo(t)
	r.write("a.md", "a\n", 0o644)
	r.commit("first")
	f := newFetcher(t, gitrepo.Options{AllowLocal: true})
	cred := &gitrepo.Credential{Password: "<placeholder-token>"}
	for _, addr := range []string{r.bare(), "git@git.example.com:team/resources.git"} {
		if _, err := f.Snapshot(context.Background(), gitrepo.Source{URL: addr, Credential: cred}); !errors.Is(err, gitrepo.ErrAddress) {
			t.Errorf("%s: err = %v, want ErrAddress", addr, err)
		}
	}
	if _, err := f.Snapshot(context.Background(), gitrepo.Source{URL: "https://git.example.com/r.git", Credential: &gitrepo.Credential{Password: "a\nb"}}); !errors.Is(err, gitrepo.ErrAddress) {
		t.Errorf("a token with a line break: err = %v, want ErrAddress", err)
	}
}

func TestCheckAddress(t *testing.T) {
	for _, c := range []struct {
		addr, want string
		local      bool
	}{
		{"https://git.example.com/r.git", "https", false},
		{"ssh://git@git.example.com/r.git", "ssh", false},
		{"git@git.example.com:team/r.git", "ssh", false},
		{"file:///srv/r.git", "file", true},
	} {
		got, err := gitrepo.CheckAddress(c.addr, c.local)
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.addr, got, err, c.want)
		}
	}
	for _, addr := range []string{"http://git.example.com/r.git", "file:///srv/r.git", "ext::sh -c x", "/srv/r.git"} {
		if _, err := gitrepo.CheckAddress(addr, false); !errors.Is(err, gitrepo.ErrAddress) {
			t.Errorf("%s: err = %v, want ErrAddress", addr, err)
		}
	}
}
