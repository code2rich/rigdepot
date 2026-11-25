// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/device"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/keyring"
)

// fakeServer answers POST /api/v1/feedback with status and keeps the bodies it got.
type fakeServer struct {
	mu     sync.Mutex
	bodies []map[string]any
	status int
}

func (s *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/api/v1/feedback" || r.Header.Get("Authorization") != "Bearer <placeholder-device-token>" {
		http.Error(w, "unexpected request", http.StatusTeapot)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.bodies = append(s.bodies, body)
	status := s.status
	s.mu.Unlock()
	if status != http.StatusCreated {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		io.WriteString(w, `{"type":"about:blank","title":"x","status":400,"code":"request.invalid","detail":"The request has invalid fields.","errors":[{"field":"resource","code":"unknown"}]}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	io.WriteString(w, `{"number":42,"title":"t","type":"bug","origin":"ai","status":"new","severity":"medium","created_at":"2026-11-25T10:00:00Z",
		"updated_at":"2026-11-25T10:00:00Z","body":"b","context":{},"events":[]}`)
}

// runSignedIn runs rgd with a stand-in for the signed-in client to url ("" for not signed in).
func (w *world) runSignedIn(url, stdin string, args ...string) result {
	var stdout, stderr bytes.Buffer
	env := Env{
		Stdin: strings.NewReader(stdin), Stdout: &stdout, Stderr: &stderr,
		Getenv: func(k string) string { return w.env[k] }, HomeDir: func() (string, error) { return w.home, nil },
		SystemLanguages: func() []string { return w.system }, IsTerminal: func(any) bool { return false }, GOOS: "linux",
	}
	if url != "" {
		env.Connect = func(context.Context) (apiclient.ClientWithResponsesInterface, error) {
			return apiclient.NewClientWithResponses(url+"/", apiclient.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
				r.Header.Set("Authorization", "Bearer <placeholder-device-token>")
				return nil
			}))
		}
	}
	code := Run(args, env)
	return result{code, stdout.String(), stderr.String()}
}

func TestFeedbackSendsOnlyTheAgreedContext(t *testing.T) {
	fs := &fakeServer{status: http.StatusCreated}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	w := newWorld(t)

	args := []string{"--json", "feedback", "--title", "var in Java 8", "--resource", "unit-testing", "--resource-version", "4",
		"--ai", "--tool", "claude-code", "--tool-version", "2.1.330", "--friction", "repeated_correction=2"}
	r := w.runSignedIn(srv.URL, "The tests did not compile.\n", args...)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	doc := oneDocument(t, args, r.stdout)
	if doc["number"] != float64(42) || doc["schema"] != "rgd.feedback/v1" {
		t.Errorf("output: %v", doc)
	}
	if len(fs.bodies) != 1 {
		t.Fatalf("requests: %d", len(fs.bodies))
	}
	got := fs.bodies[0]
	if got["body"] != "The tests did not compile.\n" || got["ai_drafted"] != true || got["resource"] != "unit-testing" || got["resource_version"] != float64(4) {
		t.Errorf("request: %v", got)
	}
	ctx, _ := got["context"].(map[string]any)
	var keys []string
	for k := range ctx {
		keys = append(keys, k)
	}
	want := map[string]bool{"cli_version": true, "os": true, "tool": true, "tool_version": true, "frictions": true}
	if len(keys) != len(want) || ctx["os"] != "linux" {
		t.Errorf("context sent: %v", ctx)
	}
	for _, k := range keys {
		if !want[k] {
			t.Errorf("context field %s was sent", k)
		}
	}

	// Without the AI options: only the program's version and the system.
	r = w.runSignedIn(srv.URL, "", "feedback", "--title", "x", "-m", "Text.")
	if r.code != 0 || !strings.Contains(r.stdout, "FB-42") || !strings.Contains(r.stdout, "cli_version, os") {
		t.Errorf("human output: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if ctx, _ := fs.bodies[1]["context"].(map[string]any); len(ctx) != 2 {
		t.Errorf("context without the AI options: %v", ctx)
	}
}

func TestFeedbackExitCodes(t *testing.T) {
	fs := &fakeServer{status: http.StatusCreated}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	w := newWorld(t)
	for _, c := range []struct {
		name string
		url  string
		args []string
		want int
	}{
		{"not signed in", "", []string{"feedback", "--title", "x", "-m", "y"}, 6},
		{"no title", srv.URL, []string{"feedback", "-m", "y"}, 2},
		{"no text", srv.URL, []string{"feedback", "--title", "x"}, 2},
		{"bad type", srv.URL, []string{"feedback", "--type", "friction", "--title", "x", "-m", "y"}, 2},
		{"bad friction", srv.URL, []string{"feedback", "--title", "x", "-m", "y", "--friction", "chat=1"}, 2},
	} {
		if r := w.runSignedIn(c.url, "", c.args...); r.code != c.want {
			t.Errorf("%s: exit %d, want %d\n%s", c.name, r.code, c.want, r.stderr)
		}
	}
	if len(fs.bodies) != 0 {
		t.Errorf("a refused command reached the server %d times", len(fs.bodies))
	}

	fs.status = http.StatusBadRequest
	r := w.runSignedIn(srv.URL, "", "feedback", "--title", "x", "-m", "y")
	if r.code != 2 || !strings.Contains(r.stderr, "resource (unknown)") {
		t.Errorf("refused field: %d %s", r.code, r.stderr)
	}
	fs.status = http.StatusUnauthorized
	if r := w.runSignedIn(srv.URL, "", "feedback", "--title", "x", "-m", "y"); r.code != 6 {
		t.Errorf("revoked token: %d %s", r.code, r.stderr)
	}
	srv.Close()
	if r := w.runSignedIn(srv.URL, "", "feedback", "--title", "x", "-m", "y"); r.code != 7 {
		t.Errorf("server down: %d %s", r.code, r.stderr)
	}
}

// TestFeedbackUsesTheDeviceSignIn: without a stand-in, rgd feedback sends the device token that
// rgd login stored, to the server that issued it.
func TestFeedbackUsesTheDeviceSignIn(t *testing.T) {
	fs := &fakeServer{status: http.StatusCreated}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	w := newWorld(t)
	if r := w.run("feedback", "--title", "x", "-m", "y"); r.code != 6 || !strings.Contains(r.stderr, "login") {
		t.Fatalf("before signing in: %d %s", r.code, r.stderr)
	}
	server, _ := apiclient.NormalServer(srv.URL)
	creds, _ := json.Marshal(device.Credentials{Server: server, DeviceID: "0193a4b2-6c1e-7a00-8000-000000000201",
		Token: "<placeholder-device-token>", UserID: "0193a4b2-6c1e-7a00-8000-000000000011", Username: "jdoe"})
	data, err := home.At(w.dataDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := keyring.Open(data, keyring.Options{}).Set(keyring.DeviceToken, creds); err != nil {
		t.Fatal(err)
	}
	if r := w.run("feedback", "--title", "x", "-m", "y"); r.code != 0 || len(fs.bodies) != 1 {
		t.Errorf("signed in: %d %s %s", r.code, r.stdout, r.stderr)
	}
}
