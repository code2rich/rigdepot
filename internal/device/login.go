// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package device

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/keyring"
)

// API is what signing in and out needs of the server; the generated client
// (apiclient.ClientWithResponses) has it.
type API interface {
	RequestDeviceCodeWithResponse(ctx context.Context, body apiclient.RequestDeviceCodeJSONRequestBody, reqEditors ...apiclient.RequestEditorFn) (*apiclient.RequestDeviceCodeResponse, error)
	PollDeviceTokenWithResponse(ctx context.Context, body apiclient.PollDeviceTokenJSONRequestBody, reqEditors ...apiclient.RequestEditorFn) (*apiclient.PollDeviceTokenResponse, error)
	SignOutDeviceWithResponse(ctx context.Context, deviceID apiclient.DeviceId, reqEditors ...apiclient.RequestEditorFn) (*apiclient.SignOutDeviceResponse, error)
}

// Computer is what the server is told about this computer, and shows on
// the confirmation page for the person to compare.
type Computer struct {
	Name    string // the host name
	OS      string // runtime.GOOS
	Arch    string // runtime.GOARCH
	Version string // the program's version, apiclient.Version
}

// Prompt is what the person needs to approve the sign-in in a browser.
type Prompt struct {
	UserCode string
	// URL is the confirmation page with the code filled in; Page is the
	// page without it, for typing the code by hand.
	URL, Page string
	ExpiresIn time.Duration
}

// LoginOptions are the parts of Login that come from outside.
type LoginOptions struct {
	// Server is the server's address, as apiclient.NormalServer writes it.
	Server   string
	Computer Computer
	// Prompt tells the person the code and where to approve it. It is called
	// once, after the server answered and before the first poll.
	Prompt func(Prompt)
	// Sleep waits d or until ctx ends; nil waits for real. Tests make it
	// return at once.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// minInterval keeps polling polite even if a server says 0.
const minInterval = time.Second

// Login signs this computer in with the device code flow
// (docs/architecture/06-server.md#rgd-login设备码流程):
//
//  1. It takes the device key from the keychain, or makes one when the
//     keychain says there is none, and stores it before anything is sent.
//  2. It sends the computer and the public key and gets the codes.
//  3. It shows them (o.Prompt) and polls every interval, slower when the
//     server says so, until the person approves or refuses, or the request
//     expires.
//  4. It stores the device token with the server and the account it belongs
//     to.
//
// It fails with *AlreadyLoggedInError when the computer is signed in
// already, *StoreError when the keychain cannot be read or written, ErrDenied
// or ErrExpired, and the server's or the network's errors.
func (id *Identity) Login(ctx context.Context, api API, o LoginOptions) (Credentials, error) {
	if o.Sleep == nil {
		o.Sleep = sleep
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	switch c, err := id.Credentials(); {
	case err == nil:
		return Credentials{}, &AlreadyLoggedInError{Credentials: c}
	case !errors.Is(err, ErrNotLoggedIn):
		return Credentials{}, err
	}
	key, err := id.ensureKey()
	if err != nil {
		return Credentials{}, err
	}

	resp, err := api.RequestDeviceCodeWithResponse(ctx, apiclient.DeviceCodeRequest{
		Name: o.Computer.Name, Os: apiclient.DeviceCodeRequestOs(o.Computer.OS), Arch: o.Computer.Arch,
		CliVersion: o.Computer.Version, PublicKey: PublicKey(key), KeyStorage: apiclient.DeviceCodeRequestKeyStorage(id.KeyStorage()),
	})
	if err != nil {
		return Credentials{}, err
	}
	if err := apiclient.Check(resp.HTTPResponse, resp.Body); err != nil {
		return Credentials{}, err
	}
	code := resp.JSON200
	if code == nil || code.DeviceCode == "" || code.UserCode == "" {
		return Credentials{}, errors.New("device: the server's answer has no device code")
	}
	interval := max(time.Duration(code.Interval)*time.Second, minInterval)
	expiresIn := time.Duration(code.ExpiresIn) * time.Second
	if o.Prompt != nil {
		o.Prompt(Prompt{UserCode: code.UserCode, URL: code.VerificationUriComplete, Page: code.VerificationUri, ExpiresIn: expiresIn})
	}
	deadline := o.Now().Add(expiresIn)

	for {
		if err := o.Sleep(ctx, interval); err != nil {
			return Credentials{}, err
		}
		if code.ExpiresIn > 0 && !o.Now().Before(deadline) {
			return Credentials{}, ErrExpired
		}
		poll, err := api.PollDeviceTokenWithResponse(ctx, apiclient.DeviceTokenRequest{DeviceCode: code.DeviceCode})
		if err != nil {
			return Credentials{}, err
		}
		err = apiclient.Check(poll.HTTPResponse, poll.Body)
		var ae *apiclient.Error
		switch {
		case err == nil && poll.JSON200 != nil:
			return id.keep(ctx, api, o.Server, poll.JSON200)
		case err == nil:
			return Credentials{}, errors.New("device: the server's answer has no device token")
		case !errors.As(err, &ae):
			return Credentials{}, err
		case ae.Code() == "device.authorization_pending":
		case ae.Code() == "device.slow_down", ae.Status == http.StatusTooManyRequests:
			// RFC 8628, 3.5: five seconds more from now on, or what the
			// server says.
			next := interval + 5*time.Second
			if v, ok := ae.Param("interval"); ok {
				if s, ok := v.(float64); ok && time.Duration(s)*time.Second > interval {
					next = time.Duration(s) * time.Second
				}
			}
			interval = next
		case ae.Code() == "device.denied":
			return Credentials{}, ErrDenied
		case ae.Code() == "device.expired":
			return Credentials{}, ErrExpired
		default:
			return Credentials{}, err
		}
	}
}

// keep stores the token the server issued. If that fails, the device is
// signed out again on the server: nobody would hold its token.
func (id *Identity) keep(ctx context.Context, api API, server string, t *apiclient.DeviceToken) (Credentials, error) {
	c := Credentials{
		Server: server, DeviceID: t.DeviceId, Token: t.DeviceToken,
		UserID: t.User.Id, Username: t.User.Username, DisplayName: t.User.DisplayName,
	}
	raw, err := json.Marshal(c)
	if err == nil {
		err = id.store.Set(keyring.DeviceToken, raw)
	}
	if err != nil {
		_, _ = api.SignOutDeviceWithResponse(ctx, c.DeviceID, bearer(c.Token))
		return Credentials{}, &StoreError{Entry: keyring.DeviceToken, Err: err}
	}
	return c, nil
}

func bearer(token string) apiclient.RequestEditorFn {
	return func(_ context.Context, r *http.Request) error {
		r.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Logout is what Logout did.
type Logout struct {
	// Credentials are the sign-in that ended; zero when there was none.
	Credentials Credentials
	// SignedIn says whether the computer was signed in.
	SignedIn bool
	// ServerErr is why the server could not sign the device out (nil when
	// it did, or when the token no longer worked anyway). The device token
	// is deleted here all the same; the person can sign the device out on
	// the server's page of keys and devices.
	ServerErr error
}

// Logout signs this computer out: it asks the server to sign the device out
// (connect returns a client for the credentials' server with their token),
// then deletes the device token and the device key from the keychain. A
// keychain that cannot be read stops it before anything is deleted.
func (id *Identity) Logout(ctx context.Context, connect func(Credentials) (API, error)) (Logout, error) {
	c, err := id.Credentials()
	if errors.Is(err, ErrNotLoggedIn) {
		// No token; a key left from a sign-in that did not finish goes.
		if _, kerr := id.PrivateKey(); kerr == nil {
			if err := id.store.Delete(keyring.DeviceKey); err != nil {
				return Logout{}, &StoreError{Entry: keyring.DeviceKey, Err: err}
			}
		} else if !errors.Is(kerr, ErrNotLoggedIn) {
			return Logout{}, kerr
		}
		return Logout{}, nil
	}
	if err != nil {
		return Logout{}, err
	}
	out := Logout{Credentials: c, SignedIn: true}
	api, err := connect(c)
	if err == nil {
		var resp *apiclient.SignOutDeviceResponse
		if resp, err = api.SignOutDeviceWithResponse(ctx, c.DeviceID); err == nil {
			err = apiclient.Check(resp.HTTPResponse, resp.Body)
		}
	}
	var ae *apiclient.Error
	if errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusNotFound) {
		err = nil // the token or the device was already gone
	}
	out.ServerErr = err
	if err := id.forget(); err != nil {
		return out, err
	}
	return out, nil
}
