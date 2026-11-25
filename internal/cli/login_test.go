// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/keyring"
)

// loginServer stands in for the server's device operations. The end-to-end
// test (test/e2e) runs the real one; this one decides what each poll gets.
type loginServer struct {
	srv      *httptest.Server
	mu       sync.Mutex
	polls    []string // problem codes; "" is the token
	request  apiclient.DeviceCodeRequest
	signOuts []string
}

func newLoginServer(t *testing.T, polls ...string) *loginServer {
	s := &loginServer{polls: polls}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		problem := func(code string) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": "Bad Request", "status": 400, "code": code})
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/device/code":
			json.NewDecoder(r.Body).Decode(&s.request)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(apiclient.DeviceCode{DeviceCode: "<placeholder: device code>", UserCode: "KQ7M-2XHP",
				VerificationUri: s.srv.URL + "/device", VerificationUriComplete: s.srv.URL + "/device?code=KQ7M-2XHP", ExpiresIn: 600, Interval: 5})
		case "POST /api/v1/device/token":
			if len(s.polls) == 0 {
				problem("device.expired")
				return
			}
			next := s.polls[0]
			s.polls = s.polls[1:]
			if next != "" {
				problem(next)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(apiclient.DeviceToken{DeviceToken: "rgdd_<placeholder: device token>", DeviceId: "0193a4b2-6c1e-7a00-8000-000000000201",
				User: apiclient.UserRef{Id: "0193a4b2-6c1e-7a00-8000-000000000011", Username: "jdoe", DisplayName: "Jamie Doe"}})
		case "DELETE /api/v1/me/devices/0193a4b2-6c1e-7a00-8000-000000000201":
			s.signOuts = append(s.signOuts, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// runFast runs the program in w without waiting between polls.
func (w *world) runFast(args ...string) result {
	var stdout, stderr bytes.Buffer
	code := Run(args, Env{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
		Getenv: func(k string) string { return w.env[k] }, HomeDir: func() (string, error) { return w.home, nil },
		SystemLanguages: func() []string { return w.system }, IsTerminal: func(any) bool { return false }, GOOS: runtime.GOOS,
		Hostname:    func() (string, error) { return "dev-laptop", nil },
		OpenBrowser: func(string) error { panic("the browser must not open in a test") },
		sleep:       func(context.Context, time.Duration) error { return nil },
	})
	return result{code, stdout.String(), stderr.String()}
}

func (w *world) keyringEntry(t *testing.T, name string) ([]byte, error) {
	t.Helper()
	data, err := home.At(w.dataDir())
	if err != nil {
		t.Fatal(err)
	}
	return keyring.Open(data, keyring.Options{}).Get(name)
}

func TestLoginAndLogout(t *testing.T) {
	s := newLoginServer(t, "device.authorization_pending", "")
	w := newWorld(t)
	r := w.runFast("login", s.srv.URL+"/")
	if r.code != 0 {
		t.Fatalf("login: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	// The address and the code are printed; nothing is opened.
	if !strings.Contains(r.stderr, s.srv.URL+"/device?code=KQ7M-2XHP") || !strings.Contains(r.stderr, "KQ7M-2XHP") {
		t.Errorf("prompt:\n%s", r.stderr)
	}
	// testenv keeps the system keychain out: the fallback is told.
	if !strings.Contains(r.stderr, "no system keychain") {
		t.Errorf("no fallback notice:\n%s", r.stderr)
	}
	if !strings.Contains(r.stdout, "Jamie Doe (jdoe)") || !strings.Contains(r.stdout, "dev-laptop") {
		t.Errorf("result: %s", r.stdout)
	}
	if s.request.Name != "dev-laptop" || s.request.KeyStorage != "file" || s.request.Os != apiclient.DeviceCodeRequestOs(runtime.GOOS) || strings.Contains(s.request.CliVersion, "+") {
		t.Errorf("code request: %+v", s.request)
	}
	// The server goes into config.toml; the token into the keychain, with
	// its server.
	cfg, _ := os.ReadFile(w.configPath())
	if !strings.Contains(string(cfg), `server = '`+s.srv.URL+`'`) && !strings.Contains(string(cfg), `server = "`+s.srv.URL+`"`) {
		t.Errorf("config.toml:\n%s", cfg)
	}
	raw, err := w.keyringEntry(t, keyring.DeviceToken)
	if err != nil || !strings.Contains(string(raw), `"server":"`+s.srv.URL+`"`) {
		t.Errorf("stored credentials: %s %v", raw, err)
	}
	if _, err := w.keyringEntry(t, keyring.DeviceKey); err != nil {
		t.Errorf("device key: %v", err)
	}

	// Again: already signed in.
	if r := w.runFast("login"); r.code != int(cliout.ExitFailure) || !strings.Contains(r.stderr, "logout") {
		t.Errorf("login twice: %d %s", r.code, r.stderr)
	}

	r = w.runFast("--json", "logout")
	doc := oneDocument(t, []string{"logout"}, r.stdout)
	if r.code != 0 || doc["wasSignedIn"] != true || doc["serverSignedOut"] != true || doc["server"] != s.srv.URL {
		t.Fatalf("logout: %d %v %s", r.code, doc, r.stderr)
	}
	if len(s.signOuts) != 1 || s.signOuts[0] != "Bearer rgdd_<placeholder: device token>" {
		t.Errorf("sign-outs: %v", s.signOuts)
	}
	for _, name := range []string{keyring.DeviceToken, keyring.DeviceKey} {
		if _, err := w.keyringEntry(t, name); !errors.Is(err, keyring.ErrNotFound) {
			t.Errorf("%s after logout: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(w.dataDir(), home.SecretsDir, keyring.MarkerFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the keyring marker is left: %v", err)
	}
	if r := w.runFast("logout"); r.code != 0 || !strings.Contains(r.stdout, "not signed in") {
		t.Errorf("logout again: %d %s", r.code, r.stdout)
	}

	// The server stays in the configuration: login needs no address now.
	s.polls = []string{""}
	if r := w.runFast("--json", "login"); r.code != 0 {
		t.Errorf("login with the configured server: %d %s %s", r.code, r.stdout, r.stderr)
	} else if doc := oneDocument(t, []string{"login"}, r.stdout); doc["server"] != s.srv.URL || doc["keyStorage"] != "file" {
		t.Errorf("login document: %v", doc)
	}
}

// The exit codes of rgd login (docs 04-cli): refused or expired 6,
// unreachable 7, no server 2.
func TestLoginExitCodes(t *testing.T) {
	cases := []struct {
		name  string
		polls []string
		args  func(s *loginServer) []string
		want  cliout.ExitCode
		code  string
	}{
		{"refused", []string{"device.authorization_pending", "device.denied"}, nil, cliout.ExitAuth, "login_denied"},
		{"expired", []string{"device.expired"}, nil, cliout.ExitAuth, "login_expired"},
		{"unreachable", nil, func(s *loginServer) []string { s.srv.Close(); return []string{"login", s.srv.URL} }, cliout.ExitUnreachable, "server_unreachable"},
		{"no server", nil, func(*loginServer) []string { return []string{"login"} }, cliout.ExitUsage, "no_server"},
		{"not a web address", nil, func(*loginServer) []string { return []string{"login", "ftp://hub.example.com"} }, cliout.ExitUsage, "bad_server"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newLoginServer(t, c.polls...)
			args := []string{"login", s.srv.URL}
			if c.args != nil {
				args = c.args(s)
			}
			w := newWorld(t)
			r := w.runFast(append([]string{"--json"}, args...)...)
			doc := oneDocument(t, args, r.stdout)
			e, _ := doc["error"].(map[string]any)
			if r.code != int(c.want) || e["code"] != c.code {
				t.Errorf("exit %d %v, want %d %s\n%s", r.code, doc, c.want, c.code, r.stderr)
			}
			if _, err := w.keyringEntry(t, keyring.DeviceToken); !errors.Is(err, keyring.ErrNotFound) {
				t.Errorf("a token after a failed login: %v", err)
			}
		})
	}
}

// The secrets rule: a keychain that cannot be read stops rgd login with
// exit code 9, without a new key pair, and rgd logout without deleting.
func TestLoginWithAnUnreadableKeychain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permissions are what makes the fallback file unreadable here")
	}
	s := newLoginServer(t, "")
	w := newWorld(t)
	if r := w.runFast("login", s.srv.URL); r.code != 0 {
		t.Fatalf("login: %d %s", r.code, r.stderr)
	}
	key := filepath.Join(w.dataDir(), home.SecretsDir, keyring.DeviceKey)
	token := filepath.Join(w.dataDir(), home.SecretsDir, keyring.DeviceToken)
	before, _ := os.ReadFile(key)
	// Readable by others: the keyring refuses the file until it is fixed,
	// so what it holds is unknown.
	if err := os.Chmod(token, 0o644); err != nil {
		t.Fatal(err)
	}
	if r := w.runFast("logout"); r.code != int(cliout.ExitStateUnknown) || len(s.signOuts) != 0 {
		t.Errorf("logout: %d %s (%d sign-outs)", r.code, r.stderr, len(s.signOuts))
	}
	if r := w.runFast("login", s.srv.URL); r.code != int(cliout.ExitStateUnknown) {
		t.Errorf("login: %d %s", r.code, r.stderr)
	}
	if err := os.Chmod(token, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.keyringEntry(t, keyring.DeviceToken); err != nil {
		t.Errorf("the token after the refusals: %v", err)
	}
	if after, _ := os.ReadFile(key); !bytes.Equal(before, after) {
		t.Error("the device key changed")
	}

	// A key left from a refused sign-in that cannot be read now is not
	// replaced by a new one, and the server is not asked.
	t.Run("key left behind", func(t *testing.T) {
		refused := newLoginServer(t, "device.denied")
		w := newWorld(t)
		if r := w.runFast("login", refused.srv.URL); r.code != int(cliout.ExitAuth) {
			t.Fatalf("refused login: %d %s", r.code, r.stderr)
		}
		key := filepath.Join(w.dataDir(), home.SecretsDir, keyring.DeviceKey)
		before, _ := os.ReadFile(key)
		os.Chmod(key, 0o644)
		refused.request = apiclient.DeviceCodeRequest{}
		if r := w.runFast("login", refused.srv.URL); r.code != int(cliout.ExitStateUnknown) || refused.request.PublicKey != "" {
			t.Errorf("login with an unreadable key: %d %s", r.code, r.stderr)
		}
		os.Chmod(key, 0o600)
		if after, _ := os.ReadFile(key); len(before) == 0 || !bytes.Equal(before, after) {
			t.Error("the device key was replaced")
		}
	})
}
