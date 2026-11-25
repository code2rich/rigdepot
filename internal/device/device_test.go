// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package device_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/device"
	"github.com/code2rich/rigdepot/internal/keyring"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

const deviceID = "0193a4b2-6c1e-7a00-8000-000000000201"

// fakeServer answers the device operations of the contract. Each poll takes
// the next answer from polls; an empty string is the token.
type fakeServer struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	polls []string // problem codes, "" for the token, "429" for too many requests
	token string

	codeRequests []apiclient.DeviceCodeRequest
	pollCount    int
	signOuts     []string // the Authorization header of each sign-out
	signOutCode  int      // the status sign-out answers; 0 is 204
}

func newFakeServer(t *testing.T, polls ...string) *fakeServer {
	f := &fakeServer{t: t, polls: polls, token: "rgdd_<placeholder: device token>"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func problem(w http.ResponseWriter, status int, code string, params map[string]any) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "code": code, "params": params})
}

func (f *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "rgd/") {
		f.t.Errorf("User-Agent %q", ua)
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/hub/api/v1/device/code":
		var req apiclient.DeviceCodeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.t.Error(err)
		}
		f.codeRequests = append(f.codeRequests, req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apiclient.DeviceCode{
			DeviceCode: "<placeholder: device code>", UserCode: "KQ7M-2XHP",
			VerificationUri: f.srv.URL + "/hub/device", VerificationUriComplete: f.srv.URL + "/hub/device?code=KQ7M-2XHP",
			ExpiresIn: 600, Interval: 5,
		})
	case r.Method == http.MethodPost && r.URL.Path == "/hub/api/v1/device/token":
		var req apiclient.DeviceTokenRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.DeviceCode != "<placeholder: device code>" {
			f.t.Errorf("polled with %q", req.DeviceCode)
		}
		if r.Header.Get("Authorization") != "" {
			f.t.Error("polling sent a credential")
		}
		f.pollCount++
		if len(f.polls) == 0 {
			f.t.Error("polled more often than expected")
			problem(w, http.StatusBadRequest, "device.expired", nil)
			return
		}
		next := f.polls[0]
		f.polls = f.polls[1:]
		switch next {
		case "":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(apiclient.DeviceToken{DeviceToken: f.token, DeviceId: deviceID,
				User: apiclient.UserRef{Id: "0193a4b2-6c1e-7a00-8000-000000000011", Username: "jdoe", DisplayName: "Jamie Doe"}})
		case "429":
			problem(w, http.StatusTooManyRequests, "rate.limited", nil)
		case "device.slow_down":
			problem(w, http.StatusBadRequest, next, map[string]any{"interval": 12})
		default:
			problem(w, http.StatusBadRequest, next, nil)
		}
	case r.Method == http.MethodDelete && r.URL.Path == "/hub/api/v1/me/devices/"+deviceID:
		f.signOuts = append(f.signOuts, r.Header.Get("Authorization"))
		if f.signOutCode != 0 {
			problem(w, f.signOutCode, "auth.required", nil)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

// server is the fake's address with a path, like a server behind a proxy
// at a sub-path.
func (f *fakeServer) server() string { return f.srv.URL + "/hub" }

func (f *fakeServer) client(t *testing.T, token string) *apiclient.ClientWithResponses {
	t.Helper()
	c, err := apiclient.Connect(apiclient.Options{Server: f.server() + "/", Token: token, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// login runs Login against f with a clock that moves with every sleep.
type run struct {
	sleeps []time.Duration
	prompt *device.Prompt
	now    time.Time
}

func (f *fakeServer) login(t *testing.T, id *device.Identity) (device.Credentials, *run, error) {
	t.Helper()
	r := &run{now: time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)}
	c, err := id.Login(context.Background(), f.client(t, ""), device.LoginOptions{
		Server:   f.server(),
		Computer: device.Computer{Name: "dev-laptop", OS: "linux", Arch: "amd64", Version: "0.5.0"},
		Prompt:   func(p device.Prompt) { r.prompt = &p },
		Sleep: func(_ context.Context, d time.Duration) error {
			r.sleeps = append(r.sleeps, d)
			r.now = r.now.Add(d)
			return nil
		},
		Now: func() time.Time { return r.now },
	})
	return c, r, err
}

// The command line's half of the device code sequence
// (docs/architecture/06-server.md#rgd-login设备码流程), step by step.
func TestLoginSequence(t *testing.T) {
	f := newFakeServer(t, "device.authorization_pending", "device.authorization_pending", "")
	store := keyring.NewMemory()
	id := device.New(store)
	c, r, err := f.login(t, id)
	if err != nil {
		t.Fatal(err)
	}

	// Step: the key pair is made and the private key stored; the public
	// key goes to the server with the computer.
	key, err := id.PrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.codeRequests) != 1 {
		t.Fatalf("%d code requests", len(f.codeRequests))
	}
	req := f.codeRequests[0]
	if req.PublicKey != device.PublicKey(key) || req.Name != "dev-laptop" || req.Os != "linux" || req.Arch != "amd64" || req.CliVersion != "0.5.0" || req.KeyStorage != "keyring" {
		t.Errorf("code request: %+v", req)
	}
	// Step: the code is shown, with where to approve it.
	if r.prompt == nil || r.prompt.UserCode != "KQ7M-2XHP" || r.prompt.URL != f.srv.URL+"/hub/device?code=KQ7M-2XHP" || r.prompt.ExpiresIn != 10*time.Minute {
		t.Errorf("prompt: %+v", r.prompt)
	}
	// Step: it polls every interval until the token comes.
	if f.pollCount != 3 || fmt.Sprint(r.sleeps) != "[5s 5s 5s]" {
		t.Errorf("%d polls, sleeps %v", f.pollCount, r.sleeps)
	}
	// Step: the token is kept with its server and account.
	want := device.Credentials{Server: f.server(), DeviceID: deviceID, Token: f.token, UserID: "0193a4b2-6c1e-7a00-8000-000000000011", Username: "jdoe", DisplayName: "Jamie Doe"}
	if c != want {
		t.Errorf("credentials: %+v", c)
	}
	if got, err := id.Credentials(); err != nil || got != want {
		t.Errorf("stored credentials: %+v %v", got, err)
	}
	if _, err := id.CredentialsFor(f.server() + "/"); err != nil {
		t.Errorf("for the same server: %v", err)
	}
	if _, err := id.CredentialsFor("https://other.example.com"); !errors.Is(err, device.ErrNotLoggedIn) {
		t.Errorf("for another server: %v", err)
	}

	// Signed in already: no second device.
	if _, _, err := f.login(t, id); !errors.As(err, new(*device.AlreadyLoggedInError)) || len(f.codeRequests) != 1 {
		t.Errorf("signing in twice: %v", err)
	}
}

// The acceptance check "polling too fast": slow_down makes every later poll
// wait longer, by what the server says or five seconds more.
func TestLoginSlowsDown(t *testing.T) {
	f := newFakeServer(t, "device.slow_down", "device.authorization_pending", "429", "")
	_, r, err := f.login(t, device.New(keyring.NewMemory()))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(r.sleeps) != "[5s 12s 12s 17s]" {
		t.Errorf("sleeps %v", r.sleeps)
	}
}

// The acceptance checks "refused" and "expired": the command line stops
// and keeps no token.
func TestLoginRefusedOrExpired(t *testing.T) {
	for _, c := range []struct {
		polls []string
		want  error
	}{
		{[]string{"device.authorization_pending", "device.denied"}, device.ErrDenied},
		{[]string{"device.expired"}, device.ErrExpired},
	} {
		f := newFakeServer(t, c.polls...)
		id := device.New(keyring.NewMemory())
		if _, _, err := f.login(t, id); !errors.Is(err, c.want) {
			t.Errorf("%v: %v", c.polls, err)
		}
		if _, err := id.Credentials(); !errors.Is(err, device.ErrNotLoggedIn) {
			t.Errorf("%v: credentials after it failed: %v", c.polls, err)
		}
	}
	// Nobody approves within the ten minutes: it stops on its own, without
	// asking any more.
	pending := make([]string, 200)
	for i := range pending {
		pending[i] = "device.authorization_pending"
	}
	f := newFakeServer(t, pending...)
	_, r, err := f.login(t, device.New(keyring.NewMemory()))
	if !errors.Is(err, device.ErrExpired) || f.pollCount != 119 || len(r.sleeps) != 120 {
		t.Errorf("local expiry: %v after %d polls and %d sleeps", err, f.pollCount, len(r.sleeps))
	}
}

// A key left from a sign-in that did not finish is used again, not
// replaced.
func TestLoginKeepsAnExistingKey(t *testing.T) {
	f := newFakeServer(t, "device.denied", "")
	id := device.New(keyring.NewMemory())
	if _, _, err := f.login(t, id); !errors.Is(err, device.ErrDenied) {
		t.Fatal(err)
	}
	key, err := id.PrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.login(t, id); err != nil {
		t.Fatal(err)
	}
	if f.codeRequests[1].PublicKey != device.PublicKey(key) {
		t.Error("a second sign-in made a new key pair")
	}
}

// The secrets rule: a keychain that cannot be read stops everything. No new
// key pair, nothing sent, nothing deleted.
func TestUnreadableKeychainStops(t *testing.T) {
	f := newFakeServer(t)
	store := keyring.NewMemory()
	id := device.New(store)
	locked := fmt.Errorf("%w: locked", keyring.ErrUnavailable)
	store.Fail(locked)
	_, _, err := f.login(t, id)
	var se *device.StoreError
	if !errors.As(err, &se) || !errors.Is(err, keyring.ErrUnavailable) {
		t.Fatalf("login with a locked keychain: %v", err)
	}
	if len(f.codeRequests) != 0 {
		t.Error("asked the server with a locked keychain")
	}
	store.Fail(nil)
	if _, err := store.Get(keyring.DeviceKey); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("a key was made while the keychain was locked: %v", err)
	}

	// Signed in, then the keychain locks: logout deletes nothing and does
	// not ask the server.
	f = newFakeServer(t, "")
	if _, _, err := f.login(t, id); err != nil {
		t.Fatal(err)
	}
	store.Fail(locked)
	calls := 0
	_, err = id.Logout(context.Background(), func(device.Credentials) (device.API, error) { calls++; return f.client(t, ""), nil })
	if !errors.Is(err, keyring.ErrUnavailable) || calls != 0 {
		t.Fatalf("logout with a locked keychain: %v (%d connections)", err, calls)
	}
	store.Fail(nil)
	if _, err := id.Credentials(); err != nil {
		t.Errorf("the token after a failed logout: %v", err)
	}
	if _, err := id.PrivateKey(); err != nil {
		t.Errorf("the key after a failed logout: %v", err)
	}
}

// tokenFails is a store that cannot keep the device token.
type tokenFails struct{ *keyring.Memory }

func (s tokenFails) Set(name string, v []byte) error {
	if name == keyring.DeviceToken {
		return errors.New("disk full")
	}
	return s.Memory.Set(name, v)
}

// A token that cannot be kept is signed out again: nobody would hold it.
func TestTokenThatCannotBeKeptIsSignedOut(t *testing.T) {
	f := newFakeServer(t, "")
	_, _, err := f.login(t, device.New(tokenFails{keyring.NewMemory()}))
	var se *device.StoreError
	if !errors.As(err, &se) || se.Entry != keyring.DeviceToken {
		t.Fatalf("login: %v", err)
	}
	if len(f.signOuts) != 1 || f.signOuts[0] != "Bearer "+f.token {
		t.Errorf("sign-outs: %v", f.signOuts)
	}
}

func TestLogout(t *testing.T) {
	ctx := context.Background()
	connect := func(t *testing.T, f *fakeServer) func(device.Credentials) (device.API, error) {
		return func(c device.Credentials) (device.API, error) {
			if c.Server != f.server() {
				t.Errorf("connects to %s", c.Server)
			}
			return f.client(t, c.Token), nil
		}
	}
	signedIn := func(t *testing.T, f *fakeServer) (*device.Identity, *keyring.Memory) {
		store := keyring.NewMemory()
		id := device.New(store)
		if _, _, err := f.login(t, id); err != nil {
			t.Fatal(err)
		}
		return id, store
	}
	gone := func(t *testing.T, store *keyring.Memory) {
		t.Helper()
		for _, name := range []string{keyring.DeviceToken, keyring.DeviceKey} {
			if _, err := store.Get(name); !errors.Is(err, keyring.ErrNotFound) {
				t.Errorf("%s after logout: %v", name, err)
			}
		}
	}

	t.Run("signs the device out on the server, then forgets it", func(t *testing.T) {
		f := newFakeServer(t, "")
		id, store := signedIn(t, f)
		out, err := id.Logout(ctx, connect(t, f))
		if err != nil || !out.SignedIn || out.ServerErr != nil || out.Credentials.DeviceID != deviceID {
			t.Fatalf("logout: %+v %v", out, err)
		}
		if len(f.signOuts) != 1 || f.signOuts[0] != "Bearer "+f.token {
			t.Errorf("sign-outs: %v", f.signOuts)
		}
		gone(t, store)
	})
	t.Run("a token the server no longer takes", func(t *testing.T) {
		f := newFakeServer(t, "")
		id, store := signedIn(t, f)
		f.signOutCode = http.StatusUnauthorized
		out, err := id.Logout(ctx, connect(t, f))
		if err != nil || out.ServerErr != nil {
			t.Fatalf("logout: %+v %v", out, err)
		}
		gone(t, store)
	})
	t.Run("the server cannot be reached", func(t *testing.T) {
		f := newFakeServer(t, "")
		id, store := signedIn(t, f)
		f.srv.Close()
		out, err := id.Logout(ctx, connect(t, f))
		if err != nil || out.ServerErr == nil || !apiclient.Unreachable(out.ServerErr) {
			t.Fatalf("logout: %+v %v", out, err)
		}
		gone(t, store)
	})
	t.Run("not signed in", func(t *testing.T) {
		f := newFakeServer(t, "device.denied")
		store := keyring.NewMemory()
		id := device.New(store)
		f.login(t, id) // leaves a key behind
		out, err := id.Logout(ctx, func(device.Credentials) (device.API, error) {
			t.Error("connected without credentials")
			return nil, nil
		})
		if err != nil || out.SignedIn {
			t.Fatalf("logout: %+v %v", out, err)
		}
		gone(t, store)
	})
}
