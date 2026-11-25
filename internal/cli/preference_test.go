// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/keyring"
)

// catalogServer stands in for the server's catalog: each resource's policy and whether it is on
// for the person; it records the preferences it is sent.
type catalogServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	on   map[string]bool
	pol  map[string]string
	puts []string
}

func newCatalogServer(t *testing.T) *catalogServer {
	s := &catalogServer{on: map[string]bool{"lint": false, "review": true, "jira": true}, pol: map[string]string{"lint": "optional", "review": "recommended", "jira": "required"}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer rgdd_<placeholder: device token>" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		code := strings.TrimPrefix(r.URL.Path, "/api/v1/catalog/")
		code, pref := strings.CutSuffix(code, "/preference")
		policy, ok := s.pol[code]
		if !ok {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": "Not Found", "status": 404, "code": "object.not_found"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if pref && r.Method == http.MethodPut {
			var body struct{ Enabled bool }
			json.NewDecoder(r.Body).Decode(&body)
			s.puts = append(s.puts, code)
			s.on[code] = body.Enabled
			json.NewEncoder(w).Encode(map[string]any{"code": code, "enabled": body.Enabled, "policy": policy})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"code": code, "kind": "skill", "name": code, "policy": policy, "enabled": s.on[code]})
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// signIn makes the world's computer signed in to server, the way rgd login leaves it.
func (w *world) signIn(t *testing.T, server string) {
	t.Helper()
	w.writeConfig(t, "server = '"+server+"'\n")
	data, err := home.At(w.dataDir())
	if err != nil {
		t.Fatal(err)
	}
	creds, _ := json.Marshal(map[string]string{"server": server, "device_id": "0193a4b2-6c1e-7a00-8000-000000000201",
		"token": "rgdd_<placeholder: device token>", "user_id": "0193a4b2-6c1e-7a00-8000-000000000011", "username": "jdoe"})
	if err := keyring.Open(data, keyring.Options{}).Set(keyring.DeviceToken, creds); err != nil {
		t.Fatal(err)
	}
}

// Signed in, rgd enable and rgd disable change the person's choice on the server and nothing
// on the computer.
func TestEnableAndDisableOnTheServer(t *testing.T) {
	s := newCatalogServer(t)
	w := newWorld(t)
	w.signIn(t, s.srv.URL)

	r := w.run("--json", "enable", "lint")
	doc := oneDocument(t, []string{"enable"}, r.stdout)
	if r.code != 0 || doc["changed"] != true || doc["delivered"] != true || doc["server"] != s.srv.URL || !s.on["lint"] {
		t.Fatalf("enable: %d %v %s", r.code, doc, r.stderr)
	}
	if r := w.run("disable", "review"); r.code != 0 || !strings.Contains(r.stdout, "all your computers") || s.on["review"] {
		t.Errorf("disable: %d %s %s", r.code, r.stdout, r.stderr)
	}
	// Again: nothing to send.
	if r := w.run("disable", "review"); r.code != 0 || len(s.puts) != 2 {
		t.Errorf("disable again: %d, %v", r.code, s.puts)
	}
	if r := w.run("--json", "disable", "jira"); r.code != int(cliout.ExitFailure) || !strings.Contains(r.stdout, `"required"`) {
		t.Errorf("a required resource: %d %s", r.code, r.stdout)
	}
	if r := w.run("--json", "enable", "nope"); r.code != int(cliout.ExitUsage) || !strings.Contains(r.stdout, "no_such_resource") {
		t.Errorf("an unknown resource: %d %s", r.code, r.stdout)
	}
	if len(s.puts) != 2 {
		t.Errorf("preferences sent: %v", s.puts)
	}

	// The server stopped accepting the token: signed out, exit 6.
	s.mu.Lock()
	s.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	s.mu.Unlock()
	if r := w.run("--json", "enable", "lint"); r.code != int(cliout.ExitAuth) || !strings.Contains(r.stdout, "signed_out") {
		t.Errorf("signed out: %d %s", r.code, r.stdout)
	}
	// Unreachable: exit 7, nothing changed.
	s.srv.Close()
	if r := w.run("--json", "enable", "lint"); r.code != int(cliout.ExitUnreachable) {
		t.Errorf("unreachable: %d %s", r.code, r.stdout)
	}
}

// A server is configured but the computer is not signed in, and there is no repository: the
// person is told to sign in (exit 6).
func TestSyncWithoutSigningIn(t *testing.T) {
	w := newWorld(t)
	w.writeConfig(t, "server = 'https://hub.example.com'\n")
	for _, args := range [][]string{{"sync"}, {"sync", "--plan"}, {"enable", "lint"}} {
		r := w.run(append([]string{"--json"}, args...)...)
		if r.code != int(cliout.ExitAuth) || !strings.Contains(r.stdout, "not_logged_in") {
			t.Errorf("%v: %d %s %s", args, r.code, r.stdout, r.stderr)
		}
	}
	// Without a server either: no source, and the message says how to sign in.
	t.Run("nothing configured", func(t *testing.T) {
		w := newWorld(t)
		if r := w.run("sync"); r.code != int(cliout.ExitUsage) || !strings.Contains(r.stderr, "login") {
			t.Errorf("%d %s", r.code, r.stderr)
		}
	})
}
