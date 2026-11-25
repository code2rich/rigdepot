// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package oidcflow is the OpenID Connect authorization code flow with PKCE that the OIDC and
// GitLab identity sources share: discovery, the authorization request, the code exchange, the
// ID token's checks, and the connection test. Tokens are checked with go-oidc; nothing here
// parses a JWT by hand.
package oidcflow

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/code2rich/rigdepot/ext/identity"
)

// Flow is what one sign-in needs: the provider, the client and how to read the ID token.
type Flow struct {
	Issuer       string // exactly as the provider's discovery document says it
	ClientID     string
	ClientSecret string
	Scopes       []string // "openid" is added when missing
	// UsernameClaims are the claims tried in order for a suggested account name.
	UsernameClaims []string
	// GroupsClaim is the claim with the person's groups; "" for none.
	GroupsClaim string
}

// Client runs flows. It keeps each provider's discovery document and keys for a while, so that
// a sign-in does not fetch them every time. The zero value is not usable; use New.
type Client struct {
	http *http.Client
	ttl  time.Duration

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	p  *oidc.Provider
	at time.Time
}

// New returns a client. Requests to providers time out after 15 seconds.
func New() *Client {
	return &Client{http: &http.Client{Timeout: 15 * time.Second}, ttl: 15 * time.Minute, cache: map[string]cached{}}
}

func (c *Client) ctx(ctx context.Context) context.Context { return oidc.ClientContext(ctx, c.http) }

// provider returns the provider at issuer from the cache, or discovers it.
func (c *Client) provider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	c.mu.Lock()
	hit, ok := c.cache[issuer]
	c.mu.Unlock()
	if ok && time.Since(hit.at) < c.ttl {
		return hit.p, nil
	}
	p, err := oidc.NewProvider(c.ctx(ctx), issuer)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cache[issuer] = cached{p: p, at: time.Now()}
	c.mu.Unlock()
	return p, nil
}

// forget drops issuer from the cache, after a sign-in failed in a way new keys or endpoints
// could explain.
func (c *Client) forget(issuer string) {
	c.mu.Lock()
	delete(c.cache, issuer)
	c.mu.Unlock()
}

func (f Flow) oauth(p *oidc.Provider, callbackURL string) *oauth2.Config {
	scopes := f.Scopes
	if !contains(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}
	return &oauth2.Config{ClientID: f.ClientID, ClientSecret: f.ClientSecret, Endpoint: p.Endpoint(), RedirectURL: callbackURL, Scopes: scopes}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// state is what Begin hands the core and Complete gets back.
type state struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Callback string `json:"r"`
	Issuer   string `json:"i"`
	ClientID string `json:"c"`
}

// Begin returns the authorization request's address and the state Complete needs.
func (c *Client) Begin(ctx context.Context, f Flow, callbackURL string) (string, []byte, error) {
	p, err := c.provider(ctx, f.Issuer)
	if err != nil {
		return "", nil, fmt.Errorf("discovery at %s: %w", f.Issuer, err)
	}
	st := state{State: rand.Text(), Nonce: rand.Text(), Verifier: oauth2.GenerateVerifier(), Callback: callbackURL, Issuer: f.Issuer, ClientID: f.ClientID}
	authURL := f.oauth(p, callbackURL).AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier))
	data, err := json.Marshal(st)
	if err != nil {
		return "", nil, err
	}
	return authURL, data, nil
}

// Complete checks the callback against the state, exchanges the code and checks the ID token.
func (c *Client) Complete(ctx context.Context, f Flow, raw []byte, query url.Values) (identity.Identity, error) {
	var st state
	if err := json.Unmarshal(raw, &st); err != nil || st.State == "" || st.Nonce == "" || st.Verifier == "" {
		return identity.Identity{}, identity.ErrState
	}
	if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(st.State)) != 1 {
		return identity.Identity{}, identity.ErrState
	}
	// The source was changed to another provider or client since the sign-in began.
	if st.Issuer != f.Issuer || st.ClientID != f.ClientID {
		return identity.Identity{}, identity.ErrState
	}
	if e := query.Get("error"); e != "" {
		if e == "access_denied" || e == "login_required" || e == "consent_required" || e == "interaction_required" {
			return identity.Identity{}, identity.ErrDeclined
		}
		return identity.Identity{}, fmt.Errorf("the identity provider answered with the error %q", short(e))
	}
	code := query.Get("code")
	if code == "" {
		return identity.Identity{}, errors.New("the identity provider sent no authorization code")
	}
	p, err := c.provider(ctx, f.Issuer)
	if err != nil {
		return identity.Identity{}, fmt.Errorf("discovery at %s: %w", f.Issuer, err)
	}
	tok, err := f.oauth(p, st.Callback).Exchange(c.ctx(ctx), code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return identity.Identity{}, fmt.Errorf("exchanging the authorization code: %w", tokenError(err))
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return identity.Identity{}, errors.New("the token response has no ID token")
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: f.ClientID}).Verify(c.ctx(ctx), rawID)
	if err != nil {
		c.forget(f.Issuer)
		return identity.Identity{}, fmt.Errorf("the ID token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(st.Nonce)) != 1 {
		return identity.Identity{}, errors.New("the ID token: its nonce is not the one of this sign-in")
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return identity.Identity{}, fmt.Errorf("the ID token: %w", err)
	}
	// With several audiences, the token must have been issued to this client (OIDC Core 3.1.3.7).
	if len(idt.Audience) > 1 {
		if azp, _ := claims["azp"].(string); azp != f.ClientID {
			return identity.Identity{}, errors.New("the ID token was issued to another client")
		}
	}
	if strings.TrimSpace(idt.Subject) == "" {
		return identity.Identity{}, errors.New("the ID token has no subject")
	}
	id := identity.Identity{Subject: idt.Subject}
	id.Email, _ = claims["email"].(string)
	id.EmailVerified = truthy(claims["email_verified"]) && id.Email != ""
	id.DisplayName, _ = claims["name"].(string)
	for _, k := range f.UsernameClaims {
		if s, _ := claims[k].(string); s != "" {
			id.Username = s
			break
		}
	}
	if f.GroupsClaim != "" {
		if gs, ok := claims[f.GroupsClaim].([]any); ok {
			for _, g := range gs {
				if s, ok := g.(string); ok && s != "" {
					id.Groups = append(id.Groups, s)
				}
			}
		}
	}
	return id, nil
}

// truthy reads email_verified, which some providers send as the string "true".
func truthy(v any) bool {
	switch v := v.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	}
	return false
}

func short(s string) string {
	if len(s) > 64 {
		return s[:64] + "…"
	}
	return s
}

// tokenError keeps the parts of a token endpoint's refusal that say what went wrong, and drops
// the body, which is the provider's text.
func tokenError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		status := ""
		if re.Response != nil {
			status = re.Response.Status
		}
		if re.ErrorCode != "" {
			return fmt.Errorf("the token endpoint refused (%s): %s", status, short(re.ErrorCode))
		}
		return fmt.Errorf("the token endpoint refused (%s)", status)
	}
	return err
}

// Test checks discovery, the key set, and whether the token endpoint accepts the client: a code
// that cannot be valid is exchanged, and the provider must refuse the code (invalid_grant), not
// the client (invalid_client).
func (c *Client) Test(ctx context.Context, f Flow, callbackURL string) error {
	discovery := func(err error) error { return &identity.TestError{Step: identity.StepDiscovery, Err: err} }
	p, err := oidc.NewProvider(c.ctx(ctx), f.Issuer)
	if err != nil {
		return discovery(err)
	}
	var meta struct {
		JWKS string `json:"jwks_uri"`
	}
	if err := p.Claims(&meta); err != nil || meta.JWKS == "" {
		return discovery(errors.New("the discovery document names no key set (jwks_uri)"))
	}
	if p.Endpoint().AuthURL == "" || p.Endpoint().TokenURL == "" {
		return discovery(errors.New("the discovery document names no authorization or token endpoint"))
	}
	if err := c.keys(ctx, meta.JWKS); err != nil {
		return discovery(err)
	}
	conf := f.oauth(p, callbackURL)
	conf.Endpoint.AuthStyle = oauth2.AuthStyleInHeader
	_, err = conf.Exchange(c.ctx(ctx), "connection-test-"+rand.Text(), oauth2.VerifierOption(oauth2.GenerateVerifier()))
	var re *oauth2.RetrieveError
	switch {
	case err == nil:
		return nil // a provider that accepts any code; the client was accepted too
	case !errors.As(err, &re):
		return discovery(fmt.Errorf("the token endpoint: %w", err))
	case re.ErrorCode == "invalid_client" || re.ErrorCode == "unauthorized_client" ||
		(re.Response != nil && re.Response.StatusCode == http.StatusUnauthorized):
		return &identity.TestError{Step: identity.StepClient, Err: tokenError(err)}
	}
	return nil
}

// keys fetches the key set and checks it has keys.
func (c *Client) keys(ctx context.Context, jwksURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("the key set: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the key set at %s answered %s", jwksURL, resp.Status)
	}
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil || len(set.Keys) == 0 {
		return fmt.Errorf("the key set at %s has no keys", jwksURL)
	}
	return nil
}

// CheckURL checks a provider address setting: an absolute https address without query or
// fragment. Plain http is allowed only on this machine (localhost, loopback addresses), for
// trying things out. It returns a *identity.ConfigError for field.
func CheckURL(field, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return &identity.ConfigError{Field: field, Code: "required"}
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return &identity.ConfigError{Field: field, Code: "format"}
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
		return &identity.ConfigError{Field: field, Code: "https"}
	}
	return &identity.ConfigError{Field: field, Code: "format"}
}

// CheckClientID checks the client ID setting.
func CheckClientID(field, id string) error {
	switch {
	case strings.TrimSpace(id) == "":
		return &identity.ConfigError{Field: field, Code: "required"}
	case len(id) > 256 || strings.ContainsAny(id, "\r\n"):
		return &identity.ConfigError{Field: field, Code: "format"}
	}
	return nil
}
