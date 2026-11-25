// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/identity/identitytest"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/server"
	"github.com/code2rich/rigdepot/internal/server/config"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// runServerEnv makes the test binary run the server program instead of the tests: the end-to-end
// test starts it as a child process, with the identity sources the program registers.
const runServerEnv = "RGD_TEST_RUN_SERVER_PROGRAM"

func TestMain(m *testing.M) {
	if os.Getenv(runServerEnv) == "1" {
		os.Exit(server.Main(os.Args[1:]))
	}
	testenv.Main(m)
}

// syncBuffer collects a child's output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestSignInWithOIDC is the whole sign-in through an OpenID Connect provider with the real
// program: the server as a process, the OIDC source it registers (builtin/all/server), and a
// fake provider on this machine. An administrator adds the source with its client secret and
// tests it; a new person signs in and gets a pending account; the administrator signs in through
// the provider with a verified address and is linked to their own account; someone whose address
// the provider did not verify gets a new account instead. The client secret is then searched for
// in every response, the server's log, the audit log and the database.
func TestSignInWithOIDC(t *testing.T) {
	idp := identitytest.NewOIDC(t, "/realms/dev")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	base := "http://" + addr
	data := filepath.Join(t.TempDir(), "data")
	const setupToken = "<placeholder: setup token>-e2e"

	cmd := exec.Command(os.Args[0], "serve")
	cmd.Env = append(os.Environ(), runServerEnv+"=1",
		config.EnvName("LISTEN")+"="+addr, config.EnvName("BASE_URL")+"="+base,
		config.EnvName("DATA_DIR")+"="+data, config.EnvName("SETUP_TOKEN")+"="+setupToken,
		config.EnvName("LOG_LEVEL")+"=debug")
	logs := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var bodies []string
	do := func(method, path string, body any, header map[string]string, want int) []byte {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		target := path
		if strings.HasPrefix(path, "/") {
			target = base + path
		}
		req, err := http.NewRequest(method, target, rd)
		if err != nil {
			t.Fatal(err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := browser.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		bodies = append(bodies, fmt.Sprint(resp.Header)+string(out))
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %s, want %d\n%s\n%s", method, path, resp.Status, want, out, logs)
		}
		if loc := resp.Header.Get("Location"); loc != "" {
			return []byte(loc)
		}
		return out
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server did not start:\n%s", logs)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The first administrator, with an address.
	do(http.MethodPost, "/api/v1/setup/admin", map[string]any{"username": "admin", "password": "<placeholder: admin password>", "email": "admin@example.com"},
		map[string]string{"X-Setup-Token": setupToken}, http.StatusCreated)

	// The OIDC source, compiled into the program.
	var kinds struct {
		Items []struct{ Kind, Name string }
	}
	if err := json.Unmarshal(do(http.MethodGet, "/api/v1/identity-providers/kinds", nil, nil, http.StatusOK), &kinds); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(kinds.Items) != "[{gitlab GitLab} {oidc OpenID Connect}]" {
		t.Errorf("kinds: %v", kinds.Items)
	}
	var created struct {
		ID              string `json:"id"`
		HasClientSecret bool   `json:"has_client_secret"`
		CallbackURL     string `json:"callback_url"`
	}
	if err := json.Unmarshal(do(http.MethodPost, "/api/v1/identity-providers", map[string]any{
		"kind": "oidc", "name": "Company SSO", "config": map[string]any{"issuer": idp.URL, "client_id": idp.ClientID}, "client_secret": idp.ClientSecret,
	}, nil, http.StatusCreated), &created); err != nil {
		t.Fatal(err)
	}
	if !created.HasClientSecret || created.CallbackURL != base+"/api/v1/auth/providers/"+created.ID+"/callback" {
		t.Errorf("created: %+v", created)
	}
	if got := string(do(http.MethodPost, "/api/v1/identity-providers/"+created.ID+"/test", nil, nil, http.StatusOK)); !strings.Contains(got, `"ok":true`) {
		t.Errorf("test: %s", got)
	}
	if meta := string(do(http.MethodGet, "/api/v1/meta", nil, nil, http.StatusOK)); !strings.Contains(meta, `{"id":"`+created.ID+`","kind":"oidc","name":"Company SSO"}`) {
		t.Errorf("meta: %s", meta)
	}

	// signIn plays a browser without a session through the provider and returns where it lands
	// and who it is then.
	signIn := func(claims map[string]any) (string, map[string]any) {
		t.Helper()
		fresh, _ := cookiejar.New(nil) // another browser, signed in nowhere
		browser.Jar = fresh
		idp.SignIn(claims)
		authURL := string(do(http.MethodGet, "/api/v1/auth/providers/"+created.ID+"/start?return_to=/catalog", nil, nil, http.StatusFound))
		if !strings.HasPrefix(authURL, idp.URL+"/authorize?") {
			t.Fatalf("start sends the browser to %s", authURL)
		}
		back := idp.Authorize(t, authURL)
		landing := string(do(http.MethodGet, "/api/v1/auth/providers/"+created.ID+"/callback?"+back.Encode(), nil, nil, http.StatusFound))
		var me map[string]any
		if err := json.Unmarshal(do(http.MethodGet, "/api/v1/me", nil, nil, http.StatusOK), &me); err != nil {
			t.Fatal(err)
		}
		return landing, me
	}

	landing, me := signIn(map[string]any{"sub": "u-1", "email": "jane@example.com", "email_verified": true, "name": "Jane Doe", "preferred_username": "jane"})
	if landing != base+"/apply" || me["status"] != "pending" || me["username"] != "jane" || me["email"] != "jane@example.com" {
		t.Errorf("someone new: lands on %s as %v", landing, me)
	}
	landing, me = signIn(map[string]any{"sub": "u-admin", "email": "Admin@example.com", "email_verified": true})
	if landing != base+"/catalog" || me["username"] != "admin" || me["platform_admin"] != true {
		t.Errorf("the administrator by a verified address: lands on %s as %v", landing, me)
	}
	landing, me = signIn(map[string]any{"sub": "u-other", "email": "admin@example.com", "email_verified": false, "preferred_username": "admin"})
	if landing != base+"/apply" || me["username"] == "admin" || me["status"] != "pending" || me["email"] != nil {
		t.Errorf("an unverified address: lands on %s as %v", landing, me)
	}

	// Back as the administrator: the audit log has the sign-ins and the source.
	browser.Jar = jar
	audit := string(do(http.MethodGet, "/api/v1/audit/export", nil, nil, http.StatusOK))
	for _, want := range []string{"identity_provider.create", "identity_provider.test", `""linked"":""email""`, `""linked"":""new""`, ",oidc"} {
		if !strings.Contains(audit, want) {
			t.Errorf("the audit log lacks %s:\n%s", want, audit)
		}
	}
	// The provider got the secret, in the token requests, and nobody else did.
	sent := false
	for _, r := range idp.Requests() {
		if r.Path == "/realms/dev/token" && r.Header.Get("Authorization") != "" {
			sent = true
		}
		if strings.Contains(r.Query.Encode(), url.QueryEscape(idp.ClientSecret)) {
			t.Errorf("the secret went to the provider in an address: %s %v", r.Path, r.Query)
		}
	}
	if !sent {
		t.Error("no token request with client credentials reached the provider")
	}

	db, err := os.ReadFile(filepath.Join(data, brand.CLILong+".db"))
	if err != nil {
		entries, _ := os.ReadDir(data)
		t.Fatalf("%v (data directory: %v)", err, entries)
	}
	for _, form := range []string{idp.ClientSecret, url.QueryEscape(idp.ClientSecret)} {
		for i, b := range bodies {
			if strings.Contains(b, form) {
				t.Errorf("response %d shows the client secret: %s", i, b)
			}
		}
		if strings.Contains(logs.String(), form) {
			t.Errorf("the server log shows the client secret")
		}
		if strings.Contains(audit, form) {
			t.Errorf("the audit log shows the client secret")
		}
		if bytes.Contains(db, []byte(form)) {
			t.Errorf("the database holds the client secret in plain text")
		}
	}
}
