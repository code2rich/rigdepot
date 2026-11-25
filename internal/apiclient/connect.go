// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package apiclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/buildinfo"
)

// DefaultTimeout limits each request, from dialling to the end of the
// answer, when Options.Timeout is zero.
const DefaultTimeout = 30 * time.Second

// Options say how Connect reaches the server.
type Options struct {
	// Server is the server's public address, such as https://hub.example.com
	// or one with a path (the API is below it). Required.
	Server string
	// Token is sent as a bearer token with every request: the device token,
	// or an access token in CI. "" sends none.
	Token string
	// ViaTool is the AI tool the program acts for (X-Via-Tool); "" for none.
	ViaTool string
	// Timeout limits each request; zero is DefaultTimeout.
	Timeout time.Duration
	// CAFile is a PEM file of certificate authorities to trust besides the
	// system's, for a server with a certificate of an internal CA.
	CAFile string
	// Transport replaces the HTTP transport; nil is a copy of the default one
	// (which honours HTTPS_PROXY and NO_PROXY).
	Transport http.RoundTripper
}

// NormalServer returns the server's address in the one form the program
// keeps it: http or https, a host, no query or fragment, no trailing slash.
func NormalServer(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", fmt.Errorf("not an http or https address: %q", raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

// Version is the program's version as the server is told it: the semantic
// version without build metadata (0.0.0-dev+abc is sent as 0.0.0-dev).
func Version() string {
	v, _, _ := strings.Cut(buildinfo.Get().Version, "+")
	return v
}

// UserAgent is what the program sends as User-Agent:
// rgd/<version> (<os>; <arch>).
func UserAgent() string {
	return fmt.Sprintf("%s/%s (%s; %s)", brand.CLI, Version(), runtime.GOOS, runtime.GOARCH)
}

// Connect returns a client for the server: the generated client with a
// timeout, the program's User-Agent, the token and the trusted certificate
// authorities.
func Connect(o Options) (*ClientWithResponses, error) {
	server, err := NormalServer(o.Server)
	if err != nil {
		return nil, err
	}
	transport := o.Transport
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		if o.CAFile != "" {
			pem, err := os.ReadFile(o.CAFile)
			if err != nil {
				return nil, fmt.Errorf("read the certificate authorities: %w", err)
			}
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("%s holds no PEM certificate", o.CAFile)
			}
			t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		transport = t
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	hc := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// A redirect could carry the token to another address; the API
		// never redirects.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	agent := UserAgent()
	return NewClientWithResponses(server+"/", WithHTTPClient(hc), WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
		r.Header.Set("User-Agent", agent)
		r.Header.Set("Accept", "application/json, application/problem+json")
		if o.Token != "" {
			r.Header.Set("Authorization", "Bearer "+o.Token)
		}
		if o.ViaTool != "" {
			r.Header.Set("X-Via-Tool", o.ViaTool)
		}
		return nil
	}))
}

// Error is an answer of the server that is not a success: its status and,
// when it sent one, its problem document.
type Error struct {
	Status  int
	Problem Problem // Code is "" when the answer had no problem document
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("the server answered %d %s", e.Status, http.StatusText(e.Status))
	if e.Problem.Code != "" {
		msg += " (" + e.Problem.Code + ")"
	}
	if e.Problem.Detail != nil && *e.Problem.Detail != "" {
		msg += ": " + *e.Problem.Detail
	}
	return msg
}

// Code is the problem's stable code, such as "auth.required"; "" without a
// problem document.
func (e *Error) Code() string { return e.Problem.Code }

// Param returns a value of the problem's params.
func (e *Error) Param(name string) (any, bool) {
	if e.Problem.Params == nil {
		return nil, false
	}
	v, ok := (*e.Problem.Params)[name]
	return v, ok
}

// Check returns nil for a successful answer (2xx) and an *Error for any
// other, with the problem document decoded from body when there is one.
func Check(r *http.Response, body []byte) error {
	if r == nil {
		return errors.New("apiclient: no answer")
	}
	if r.StatusCode >= 200 && r.StatusCode < 300 {
		return nil
	}
	e := &Error{Status: r.StatusCode}
	if strings.Contains(r.Header.Get("Content-Type"), "json") {
		_ = json.Unmarshal(body, &e.Problem)
	}
	return e
}

// Unreachable reports whether err means the server could not be reached or
// did not answer: the network, a timeout, a proxy or gateway in front of it
// that has no server behind it. Such failures leave everything as it was.
func Unreachable(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		switch e.Status {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		return false
	}
	var ue *url.Error
	var ne net.Error
	return errors.As(err, &ue) || errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded)
}
