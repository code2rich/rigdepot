// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package identitytest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// OIDC is a minimal OpenID Connect provider on this machine, for tests: discovery, a key set,
// an authorization endpoint that signs in whoever SignIn named without asking, and a token
// endpoint that checks the client, the code, the redirect address and the PKCE verifier and
// returns an ID token it signs itself (RS256). It insists on what a real provider may insist
// on: PKCE with S256, the openid scope, a registered client.
type OIDC struct {
	// URL is the issuer: the server's address, with Path when one was given.
	URL string
	// ClientID and ClientSecret are the one client the provider knows.
	ClientID, ClientSecret string

	srv  *httptest.Server
	key  *rsa.PrivateKey
	kid  string
	mu   sync.Mutex
	who  map[string]any // the claims of the person who signs in next
	over map[string]any // claims that replace the computed ones (to forge tokens)
	no   string         // the error the authorization endpoint answers with, when set

	codes    map[string]grant
	requests []Request
}

// Request is a request the provider received, for tests that check what was sent.
type Request struct {
	Method, Path string
	Query        url.Values
	Form         url.Values // the body of a form POST
	Header       http.Header
}

type grant struct {
	redirectURI, challenge, nonce string
	claims                        map[string]any
	used                          bool
}

// NewOIDC starts a provider whose issuer is its address followed by path ("" or "/realms/dev").
// It stops when the test ends.
func NewOIDC(t testing.TB, path string) *OIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	o := &OIDC{
		ClientID: "rigdepot-test", ClientSecret: "client-secret-" + rand.Text(),
		key: key, kid: "k-" + rand.Text()[:8], codes: map[string]grant{},
		who: map[string]any{"sub": "subject-1", "email": "person@example.com", "email_verified": true, "name": "Test Person", "preferred_username": "person"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+path+"/.well-known/openid-configuration", o.discovery)
	mux.HandleFunc("GET "+path+"/jwks", o.jwks)
	mux.HandleFunc("GET "+path+"/authorize", o.authorize)
	mux.HandleFunc("POST "+path+"/token", o.token)
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		req := Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone()}
		if r.Method == http.MethodPost {
			req.Form = r.PostForm
		}
		o.mu.Lock()
		o.requests = append(o.requests, req)
		o.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(o.srv.Close)
	o.URL = o.srv.URL + path
	return o
}

// SignIn sets the claims of whoever signs in next: sub, email, email_verified, name and any
// other. They replace the defaults entirely.
func (o *OIDC) SignIn(claims map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.who = claims
}

// Forge makes the next ID tokens carry these claims instead of the right ones (a wrong aud,
// nonce, iss or exp), to check that they are refused. Nil stops forging.
func (o *OIDC) Forge(claims map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.over = claims
}

// Refuse makes the authorization endpoint send the browser back with this OAuth error (such as
// "access_denied"); "" signs people in again.
func (o *OIDC) Refuse(code string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.no = code
}

// Requests returns every request the provider received so far.
func (o *OIDC) Requests() []Request {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.requests)
}

// Authorize plays the browser at the provider: it opens authURL (what Begin returned) and
// returns the query the provider sends the browser back to the callback with. It fails the test
// when the provider does not redirect.
func (o *OIDC) Authorize(t testing.TB, authURL string) url.Values {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("the provider answered %s to %s", resp.Status, authURL)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc.Query()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (o *OIDC) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                o.URL,
		"authorization_endpoint":                o.URL + "/authorize",
		"token_endpoint":                        o.URL + "/token",
		"jwks_uri":                              o.URL + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"scopes_supported":                      []string{"openid", "profile", "email"},
	})
}

func (o *OIDC) jwks(w http.ResponseWriter, _ *http.Request) {
	b64 := base64.RawURLEncoding.EncodeToString
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": o.kid,
		"n": b64(o.key.N.Bytes()), "e": b64(big.NewInt(int64(o.key.E)).Bytes()),
	}}})
}

func (o *OIDC) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || !redirect.IsAbs() || q.Get("client_id") != o.ClientID {
		http.Error(w, "unknown client or redirect_uri", http.StatusBadRequest)
		return
	}
	back := redirect.Query()
	back.Set("state", q.Get("state"))
	o.mu.Lock()
	no := o.no
	o.mu.Unlock()
	switch {
	case no != "":
		back.Set("error", no)
	case q.Get("response_type") != "code" || !slices.Contains(strings.Fields(q.Get("scope")), "openid"):
		back.Set("error", "invalid_request")
	case q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256":
		// PKCE is required here, as it is at providers configured for public clients.
		back.Set("error", "invalid_request")
		back.Set("error_description", "PKCE with S256 is required")
	default:
		code := rand.Text()
		o.mu.Lock()
		claims := map[string]any{}
		for k, v := range o.who {
			claims[k] = v
		}
		o.codes[code] = grant{redirectURI: redirect.String(), challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), claims: claims}
		o.mu.Unlock()
		back.Set("code", code)
	}
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (o *OIDC) token(w http.ResponseWriter, r *http.Request) {
	oauthErr := func(status int, code string) {
		writeJSON(w, status, map[string]string{"error": code})
	}
	id, secret, basic := r.BasicAuth()
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != o.ClientID || subtle.ConstantTimeCompare([]byte(secret), []byte(o.ClientSecret)) != 1 {
		oauthErr(http.StatusUnauthorized, "invalid_client")
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		oauthErr(http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	o.mu.Lock()
	g, ok := o.codes[r.PostForm.Get("code")]
	if ok {
		o.codes[r.PostForm.Get("code")] = grant{used: true}
	}
	over := o.over
	o.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	switch {
	case !ok || g.used:
		oauthErr(http.StatusBadRequest, "invalid_grant")
		return
	case g.redirectURI != r.PostForm.Get("redirect_uri"):
		oauthErr(http.StatusBadRequest, "invalid_grant")
		return
	case base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
		oauthErr(http.StatusBadRequest, "invalid_grant")
		return
	}
	now := time.Now()
	claims := map[string]any{"iss": o.URL, "aud": o.ClientID, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()}
	if g.nonce != "" {
		claims["nonce"] = g.nonce
	}
	for k, v := range g.claims {
		claims[k] = v
	}
	for k, v := range over {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	idToken, err := o.sign(claims)
	if err != nil {
		oauthErr(http.StatusInternalServerError, "server_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": "access-" + rand.Text(), "token_type": "Bearer", "expires_in": 300, "id_token": idToken,
	})
}

// sign makes a JWS compact serialization of claims with the provider's key.
func (o *OIDC) sign(claims map[string]any) (string, error) {
	b64 := base64.RawURLEncoding.EncodeToString
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": o.kid})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, o.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return input + "." + b64(sig), nil
}
