// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/hpke"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/localsecrets"
)

// secretMarker is the value the tests set: no output, local file or log may hold it in the
// clear.
const secretMarker = "Vb8-cli-secret-marker-2Lk7-not-a-token"

const (
	fakeUser   = "0193a4b2-6c1e-7a00-8000-000000000011"
	fakeDevice = "0193a4b2-6c1e-7a00-8000-000000000201"
	fakeToken  = "rgdd_<placeholder: device token>"
)

type storedSecret struct {
	value   string
	version int
}

// secretServer stands in for the server's device sign-in and personal secret operations. It
// seals values to the public key the command line signed in with, as the real one does (the
// server's own tests check the real one).
type secretServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	pub    *ecdh.PublicKey
	needed []string // the secrets the person's resources need
	values map[string]storedSecret
	puts   []string // the values received, to check what came in
}

func newSecretServer(t *testing.T, needed ...string) *secretServer {
	s := &secretServer{needed: needed, values: map[string]storedSecret{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *secretServer) set(name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[name] = storedSecret{value: value, version: s.values[name].version + 1}
}

func (s *secretServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	problem := func(status int, code string) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "code": code})
	}
	route := r.Method + " " + r.URL.Path
	switch route {
	case "POST /api/v1/device/code":
		var req apiclient.DeviceCodeRequest
		json.NewDecoder(r.Body).Decode(&req)
		raw, _ := base64.RawURLEncoding.DecodeString(req.PublicKey)
		s.pub, _ = ecdh.X25519().NewPublicKey(raw)
		writeJSON(200, apiclient.DeviceCode{DeviceCode: "<placeholder: device code>", UserCode: "KQ7M-2XHP",
			VerificationUri: s.srv.URL + "/device", VerificationUriComplete: s.srv.URL + "/device?code=KQ7M-2XHP", ExpiresIn: 600, Interval: 5})
		return
	case "POST /api/v1/device/token":
		writeJSON(200, apiclient.DeviceToken{DeviceToken: fakeToken, DeviceId: fakeDevice,
			User: apiclient.UserRef{Id: fakeUser, Username: "jdoe", DisplayName: "Jamie Doe"}})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		problem(401, "auth.required")
		return
	}
	const secretsPath = "/api/v1/me/secrets/"
	switch {
	case route == "DELETE /api/v1/me/devices/"+fakeDevice:
		w.WriteHeader(204)
	case route == "GET /api/v1/me/secrets":
		out := apiclient.MySecretList{Items: []apiclient.MySecret{}}
		for _, n := range s.needed {
			v, ok := s.values[n]
			m := apiclient.MySecret{Name: n, Label: n, Configured: ok, UsedBy: []apiclient.ResourceRef{{Code: "jira", Kind: "mcp", Name: "Jira"}}}
			if ok {
				at := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
				m.UpdatedAt = &at
				_ = v
			} else {
				howto := "Jira, Account settings, Security, API tokens"
				m.Howto = &howto
			}
			out.Items = append(out.Items, m)
		}
		writeJSON(200, out)
	case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, secretsPath):
		name := strings.TrimPrefix(r.URL.Path, secretsPath)
		if !slices.Contains(s.needed, name) {
			problem(404, "object.not_found")
			return
		}
		var req apiclient.SetSecretRequest
		json.NewDecoder(r.Body).Decode(&req)
		s.puts = append(s.puts, req.Value)
		s.values[name] = storedSecret{value: req.Value, version: s.values[name].version + 1}
		at := time.Now().UTC()
		writeJSON(200, apiclient.MySecret{Name: name, Label: name, Configured: true, UpdatedAt: &at, UsedBy: []apiclient.ResourceRef{{Code: "jira", Kind: "mcp", Name: "Jira"}}})
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, secretsPath):
		name := strings.TrimPrefix(r.URL.Path, secretsPath)
		if _, ok := s.values[name]; !ok {
			problem(404, "object.not_found")
			return
		}
		delete(s.values, name)
		w.WriteHeader(204)
	case route == "POST /api/v1/sync/secrets":
		var req apiclient.SealedSecretsRequest
		json.NewDecoder(r.Body).Decode(&req)
		out := apiclient.SealedSecrets{DeviceId: fakeDevice, Suite: localsecrets.Suite, Items: []apiclient.SealedSecret{}, Missing: []string{}}
		names := []string{}
		if req.Names != nil {
			names = *req.Names
		} else {
			for n := range s.values {
				names = append(names, n)
			}
			slices.Sort(names)
		}
		for _, n := range names {
			v, ok := s.values[n]
			if !ok {
				out.Missing = append(out.Missing, n)
				continue
			}
			out.Items = append(out.Items, s.seal(n, v))
		}
		writeJSON(200, out)
	default:
		problem(404, "not_found")
	}
}

func (s *secretServer) seal(name string, v storedSecret) apiclient.SealedSecret {
	pk, err := hpke.NewDHKEMPublicKey(s.pub)
	if err != nil {
		panic(err)
	}
	info := localsecrets.Info(fakeUser, fakeDevice, name, v.version)
	enc, sender, err := hpke.NewSender(pk, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(info))
	if err != nil {
		panic(err)
	}
	ct, err := sender.Seal(nil, []byte(v.value))
	if err != nil {
		panic(err)
	}
	b := base64.RawURLEncoding.EncodeToString
	return apiclient.SealedSecret{Name: name, Version: v.version, Enc: b(enc), Ciphertext: b(ct), Info: info}
}

// runIn runs the program with stdin, without waiting between polls.
func (w *world) runIn(stdin string, args ...string) result {
	var stdout, stderr bytes.Buffer
	code := Run(args, Env{
		Stdin: strings.NewReader(stdin), Stdout: &stdout, Stderr: &stderr,
		Getenv: func(k string) string { return w.env[k] }, HomeDir: func() (string, error) { return w.home, nil },
		SystemLanguages: func() []string { return w.system }, IsTerminal: func(any) bool { return false }, GOOS: runtime.GOOS,
		Hostname:    func() (string, error) { return "dev-laptop", nil },
		OpenBrowser: func(string) error { panic("the browser must not open in a test") },
		sleep:       func(context.Context, time.Duration) error { return nil },
	})
	return result{code, stdout.String(), stderr.String()}
}

// filesHolding returns the files below dir that hold s.
func filesHolding(t *testing.T, dir, s string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, err := os.ReadFile(p); err == nil && bytes.Contains(data, []byte(s)) {
				out = append(out, p)
			}
		}
		return nil
	})
	return out
}

func TestSecretCommands(t *testing.T) {
	s := newSecretServer(t, "JIRA_API_TOKEN", "FIGMA_TOKEN")
	w := newWorld(t)

	// Not signed in: 6, and how to.
	if r := w.runIn("", "--json", "secret", "list"); r.code != int(cliout.ExitAuth) || !strings.Contains(r.stdout, "not_logged_in") {
		t.Fatalf("not signed in: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if r := w.runFast("login", s.srv.URL); r.code != 0 {
		t.Fatalf("login: %d %s", r.code, r.stderr)
	}

	// Set from standard input: the server gets the value without the line break; this computer
	// gets its sealed copy; nothing prints the value.
	r := w.runIn(secretMarker+"\n", "--json", "secret", "set", "JIRA_API_TOKEN", "--expires", "2027-05-01")
	doc := oneDocument(t, []string{"secret", "set"}, r.stdout)
	if r.code != 0 || doc["configured"] != true || doc["local"] != true || doc["version"] != float64(1) {
		t.Fatalf("set: %d %v %s", r.code, doc, r.stderr)
	}
	if len(s.puts) != 1 || s.puts[0] != secretMarker {
		t.Errorf("the server got %q", s.puts)
	}
	if strings.Contains(r.stdout+r.stderr, secretMarker) {
		t.Error("the output holds the value")
	}
	if fs := filesHolding(t, w.home, secretMarker); len(fs) != 0 {
		t.Errorf("files hold the value: %v", fs)
	}
	vault := filepath.Join(w.dataDir(), home.SecretsDir, localsecrets.FileName)
	if _, err := os.Stat(vault); err != nil {
		t.Fatalf("no vault: %v", err)
	}

	// Refused before anything is sent: a bad name, an empty value, a name nothing needs.
	if r := w.runIn("x\n", "secret", "set", "jira_token"); r.code != int(cliout.ExitUsage) {
		t.Errorf("bad name: %d %s", r.code, r.stderr)
	}
	if r := w.runIn("\n", "secret", "set", "FIGMA_TOKEN"); r.code != int(cliout.ExitUsage) {
		t.Errorf("empty value: %d %s", r.code, r.stderr)
	}
	if r := w.runIn("x\n", "secret", "set", "OTHER_TOKEN"); r.code != int(cliout.ExitFailure) || !strings.Contains(r.stderr, "OTHER_TOKEN") {
		t.Errorf("not needed: %d %s", r.code, r.stderr)
	}

	// List: what is set, what has a local copy, where to get the rest.
	r = w.runIn("", "--json", "secret", "list")
	var list struct {
		Secrets []secretDoc `json:"secrets"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &list); err != nil || r.code != 0 || len(list.Secrets) != 2 {
		t.Fatalf("list: %d %s %v", r.code, r.stdout, err)
	}
	for _, d := range list.Secrets {
		switch d.Name {
		case "JIRA_API_TOKEN":
			if !d.Configured || !d.Local || d.Version != 1 {
				t.Errorf("JIRA_API_TOKEN: %+v", d)
			}
		case "FIGMA_TOKEN":
			if d.Configured || d.Local || d.Howto == "" {
				t.Errorf("FIGMA_TOKEN: %+v", d)
			}
		}
	}
	if r := w.runIn("", "secret", "list"); !strings.Contains(r.stdout, "not set") || !strings.Contains(r.stdout, "Where to get one") {
		t.Errorf("list for people:\n%s", r.stdout)
	}

	// Set in the browser, then pulled: the copy follows; deleted on the server, then pulled: it
	// goes.
	s.set("FIGMA_TOKEN", "figd-"+secretMarker)
	if r := w.runIn("", "secret", "pull"); r.code != 0 || !strings.Contains(r.stdout, "2 secrets") {
		t.Errorf("pull: %d %s %s", r.code, r.stdout, r.stderr)
	}
	s.mu.Lock()
	delete(s.values, "FIGMA_TOKEN")
	s.mu.Unlock()
	w.runIn("", "secret", "pull")
	if f, _ := localsecrets.New(mustHome(t, w)).Read(); len(f.Items) != 1 || f.Items[0].Name != "JIRA_API_TOKEN" {
		t.Errorf("after pull: %+v", f.Items)
	}

	// rm deletes on the server and here.
	if r := w.runIn("", "secret", "rm", "JIRA_API_TOKEN"); r.code != 0 {
		t.Errorf("rm: %d %s", r.code, r.stderr)
	}
	if f, _ := localsecrets.New(mustHome(t, w)).Read(); len(f.Items) != 0 {
		t.Errorf("after rm: %+v", f.Items)
	}
	if r := w.runIn("", "secret", "rm", "JIRA_API_TOKEN"); r.code != 0 || !strings.Contains(r.stdout, "was not set") {
		t.Errorf("rm again: %d %s", r.code, r.stdout)
	}

	// An unreadable vault is not taken for an empty one.
	os.WriteFile(vault, []byte("{broken"), 0o600)
	if r := w.runIn("", "secret", "list"); r.code != int(cliout.ExitStateUnknown) {
		t.Errorf("unreadable vault: %d %s", r.code, r.stderr)
	}
	if data, _ := os.ReadFile(vault); string(data) != "{broken" {
		t.Errorf("rewritten: %s", data)
	}

	// Logout deletes the copies with the key.
	w.runIn("y\n", "secret", "pull") // replaces the broken file as a whole
	if r := w.runFast("logout"); r.code != 0 {
		t.Fatalf("logout: %d %s", r.code, r.stderr)
	}
	if _, err := os.Stat(vault); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("vault after logout: %v", err)
	}
}

func mustHome(t *testing.T, w *world) home.Dir {
	t.Helper()
	d, err := home.At(w.dataDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}
