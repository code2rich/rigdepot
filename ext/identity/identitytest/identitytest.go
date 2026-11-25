// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package identitytest is the test suite for identity sources. Version: v0.
//
// A source's test file starts with
//
//	func TestMain(m *testing.M) { identitytest.Main(m) }
//
//	func TestContract(t *testing.T) { identitytest.Run(t, New()) }
//
// An OIDC-based source also runs the whole sign-in against the local provider OIDC:
//
//	func TestOIDC(t *testing.T) {
//		identitytest.RunOIDC(t, New(), func(o *identitytest.OIDC) map[string]any {
//			return map[string]any{"issuer": o.URL, "client_id": o.ClientID}
//		})
//	}
//
// Unlike the package it tests, this package depends on the core. It is only ever imported from
// test files.
package identitytest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/identity"
	"github.com/code2rich/rigdepot/ext/internal/registry"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// Main runs the tests of a package in a temporary home with the network limited to this
// machine, like every test in this repository.
func Main(m *testing.M) {
	testenv.Main(m)
}

// Run checks what every source must do, without signing anyone in:
//
//   - its ID has the shape every registry requires, and it has a name;
//   - its ConfigSchema is a JSON Schema of an object that allows no other properties and does
//     not ask for the client secret, which the core keeps apart;
//   - with empty settings, Validate returns a *ConfigError, Test a *TestError of StepConfig,
//     and Begin and Complete fail, quickly, instead of panicking or going somewhere by default.
func Run(t *testing.T, p identity.Provider) {
	t.Helper()
	for _, problem := range Check(p) {
		t.Error(problem)
	}
}

// Check returns what is wrong with p, as sentences; empty when nothing is. Run reports them.
func Check(p identity.Provider) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	if !registry.ValidID(p.ID()) {
		add("ID %q: use lower-case letters, digits and single hyphens", p.ID())
	}
	if strings.TrimSpace(p.Name()) == "" {
		add("Name is empty")
	}
	var schema struct {
		Type       string                     `json:"type"`
		Additional *bool                      `json:"additionalProperties"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(p.ConfigSchema(), &schema); err != nil || schema.Type != "object" {
		add("ConfigSchema is not a JSON Schema with \"type\": \"object\" (%v)", err)
	} else {
		if schema.Additional == nil || *schema.Additional {
			add("ConfigSchema must set \"additionalProperties\": false, so that nothing secret is saved among the settings")
		}
		for name := range schema.Properties {
			if strings.Contains(strings.ToLower(name), "secret") || strings.Contains(strings.ToLower(name), "password") {
				add("ConfigSchema has the property %q; the client secret is Config.ClientSecret, kept apart by the core", name)
			}
		}
	}
	empty := identity.Config{Settings: map[string]any{}}
	var cerr *identity.ConfigError
	if err := p.Validate(empty); !errors.As(err, &cerr) {
		add("Validate with empty settings = %v, want a *identity.ConfigError", err)
	}
	err := call(func(ctx context.Context) error { return p.Test(ctx, empty, "http://127.0.0.1:1/callback") })
	var terr *identity.TestError
	if !errors.As(err, &terr) || terr.Step != identity.StepConfig {
		add("Test with empty settings = %v, want a *identity.TestError of step %q", err, identity.StepConfig)
	}
	err = call(func(ctx context.Context) error {
		_, _, err := p.Begin(ctx, empty, "http://127.0.0.1:1/callback")
		return err
	})
	if err == nil || errors.Is(err, errHung) {
		add("Begin with empty settings = %v; it must fail", err)
	}
	err = call(func(ctx context.Context) error {
		_, err := p.Complete(ctx, empty, nil, url.Values{"code": {"x"}, "state": {"y"}})
		return err
	})
	if err == nil || errors.Is(err, errHung) {
		add("Complete with empty settings and no state = %v; it must fail", err)
	}
	return out
}

var errHung = errors.New("did not return within 10 seconds")

// call runs fn with a time limit and turns a panic into an error.
func call(fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Errorf("panicked: %v", p)
			}
		}()
		done <- fn(ctx)
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		return errHung
	}
}

// RunOIDC runs the whole sign-in of an OIDC-based source against the local provider OIDC, and
// the ways it must fail. settings returns the source's settings for that provider; the client
// secret is the provider's.
func RunOIDC(t *testing.T, p identity.Provider, settings func(o *OIDC) map[string]any) {
	t.Helper()
	const callback = "http://127.0.0.1:8080/api/v1/auth/providers/test/callback"
	ctx := context.Background()
	setup := func(t *testing.T) (*OIDC, identity.Config) {
		o := NewOIDC(t, "/oidc")
		return o, identity.Config{Settings: settings(o), ClientSecret: o.ClientSecret}
	}
	// noSecret fails the test when err shows the client secret.
	noSecret := func(t *testing.T, o *OIDC, err error) {
		t.Helper()
		if err != nil && strings.Contains(err.Error(), o.ClientSecret) {
			t.Errorf("the error shows the client secret: %v", err)
		}
	}
	begin := func(t *testing.T, o *OIDC, cfg identity.Config) (string, []byte) {
		t.Helper()
		if err := p.Validate(cfg); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		authURL, state, err := p.Begin(ctx, cfg, callback)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if strings.Contains(authURL, o.ClientSecret) || strings.Contains(string(state), o.ClientSecret) {
			t.Fatal("Begin put the client secret into the address or the state")
		}
		return authURL, state
	}
	signIn := func(t *testing.T, o *OIDC, cfg identity.Config) (identity.Identity, error) {
		t.Helper()
		authURL, state := begin(t, o, cfg)
		id, err := p.Complete(ctx, cfg, state, o.Authorize(t, authURL))
		noSecret(t, o, err)
		return id, err
	}

	t.Run("signs in", func(t *testing.T) {
		o, cfg := setup(t)
		authURL, _ := begin(t, o, cfg)
		u, err := url.Parse(authURL)
		if err != nil || !strings.HasPrefix(authURL, o.URL+"/") {
			t.Fatalf("Begin sends the browser to %s, not to the provider %s", authURL, o.URL)
		}
		q := u.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("nonce") == "" || q.Get("redirect_uri") != callback {
			t.Errorf("the authorization request lacks PKCE, state, nonce or the callback: %s", authURL)
		}
		id, err := signIn(t, o, cfg)
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if id.Subject != "subject-1" || id.Email != "person@example.com" || !id.EmailVerified || id.DisplayName != "Test Person" {
			t.Errorf("identity = %+v", id)
		}
	})
	t.Run("an address the provider did not verify is not verified", func(t *testing.T) {
		for name, claims := range map[string]map[string]any{
			"false":   {"sub": "s", "email": "person@example.com", "email_verified": false},
			"missing": {"sub": "s", "email": "person@example.com"},
		} {
			t.Run(name, func(t *testing.T) {
				o, cfg := setup(t)
				o.SignIn(claims)
				id, err := signIn(t, o, cfg)
				if err != nil {
					t.Fatal(err)
				}
				if id.EmailVerified {
					t.Errorf("email_verified %s came back as verified", name)
				}
			})
		}
	})
	t.Run("the person declines", func(t *testing.T) {
		o, cfg := setup(t)
		o.Refuse("access_denied")
		if _, err := signIn(t, o, cfg); !errors.Is(err, identity.ErrDeclined) {
			t.Errorf("Complete = %v, want ErrDeclined", err)
		}
	})
	t.Run("a callback of another sign-in", func(t *testing.T) {
		o, cfg := setup(t)
		_, mine := begin(t, o, cfg)
		other, _ := begin(t, o, cfg)
		_, err := p.Complete(ctx, cfg, mine, o.Authorize(t, other))
		if !errors.Is(err, identity.ErrState) {
			t.Errorf("Complete = %v, want ErrState", err)
		}
	})
	t.Run("a code used twice", func(t *testing.T) {
		o, cfg := setup(t)
		authURL, state := begin(t, o, cfg)
		q := o.Authorize(t, authURL)
		if _, err := p.Complete(ctx, cfg, state, q); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Complete(ctx, cfg, state, q); err == nil {
			t.Error("the same code signed in twice")
		}
	})
	t.Run("a wrong client secret", func(t *testing.T) {
		o, cfg := setup(t)
		authURL, state := begin(t, o, cfg)
		wrong := cfg
		wrong.ClientSecret = "not-the-secret"
		if _, err := p.Complete(ctx, wrong, state, o.Authorize(t, authURL)); err == nil {
			t.Error("signed in with a wrong client secret")
		}
	})
	for name, forged := range map[string]map[string]any{
		"another audience": {"aud": "someone-else"},
		"another issuer":   {"iss": "https://issuer.example.com"},
		"expired":          {"exp": time.Now().Add(-time.Hour).Unix()},
		"another nonce":    {"nonce": "not-the-nonce"},
		"no nonce":         {"nonce": nil},
		"no subject":       {"sub": ""},
	} {
		t.Run("an ID token with "+name, func(t *testing.T) {
			o, cfg := setup(t)
			o.Forge(forged)
			if id, err := signIn(t, o, cfg); err == nil {
				t.Errorf("signed in as %+v", id)
			}
		})
	}
	t.Run("Test", func(t *testing.T) {
		o, cfg := setup(t)
		if err := p.Test(ctx, cfg, callback); err != nil {
			t.Errorf("Test with the right settings: %v", err)
		}
		wrong := cfg
		wrong.ClientSecret = "not-the-secret"
		var terr *identity.TestError
		if err := p.Test(ctx, wrong, callback); !errors.As(err, &terr) || terr.Step != identity.StepClient {
			t.Errorf("Test with a wrong secret = %v, want step %q", err, identity.StepClient)
		}
		noSecret(t, o, p.Test(ctx, wrong, callback))
		gone := NewOIDC(t, "/oidc")
		gcfg := identity.Config{Settings: settings(gone), ClientSecret: gone.ClientSecret}
		gone.srv.Close()
		if err := p.Test(ctx, gcfg, callback); !errors.As(err, &terr) || terr.Step != identity.StepDiscovery {
			t.Errorf("Test against a provider that does not answer = %v, want step %q", err, identity.StepDiscovery)
		}
	})
}
