// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

func devicePublicKey(t *testing.T) string {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
}

func (f *apiFixture) deviceCode(t *testing.T) httpapi.DeviceCode {
	t.Helper()
	body := `{"name":"dev-laptop","os":"linux","arch":"amd64","cli_version":"0.5.0","public_key":"` + devicePublicKey(t) + `","key_storage":"file"}`
	rec, p := f.do(t, http.MethodPost, "/api/v1/device/code", "", body, nil)
	var code httpapi.DeviceCode
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &code) != nil {
		t.Fatalf("device code: %d %+v %s", rec.Code, p, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("device code answer may be cached: %q", rec.Header().Get("Cache-Control"))
	}
	return code
}

func (f *apiFixture) poll(t *testing.T, deviceCode string) (int, httpapi.Problem, httpapi.DeviceToken) {
	t.Helper()
	rec, p := f.do(t, http.MethodPost, "/api/v1/device/token", "", `{"device_code":"`+deviceCode+`"}`, nil)
	var tok httpapi.DeviceToken
	if rec.Code == http.StatusOK {
		json.Unmarshal(rec.Body.Bytes(), &tok)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("poll answer may be cached: %q", rec.Header().Get("Cache-Control"))
	}
	return rec.Code, p, tok
}

// The device code flow over HTTP, as the contract describes it: the codes,
// RFC 8628's 400 answers with problem documents, the confirmation page's
// operations that only take a browser session, the token, my devices and
// signing a device out.
func TestDeviceCodeFlowAPI(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{realDevices: true})
	bearer := func(token string) map[string]string { return map[string]string{"Authorization": "Bearer " + token} }

	waiting := f.deviceCode(t)
	if waiting.VerificationUri != "http://127.0.0.1:8080/device" || waiting.VerificationUriComplete != waiting.VerificationUri+"?code="+waiting.UserCode ||
		waiting.Interval != 5 || waiting.ExpiresIn != 600 || len(waiting.UserCode) != 9 || waiting.UserCode[4] != '-' {
		t.Fatalf("device code: %+v", waiting)
	}
	if code, p, _ := f.poll(t, waiting.DeviceCode); code != http.StatusBadRequest || p.Code != "device.authorization_pending" || p.Detail == nil {
		t.Fatalf("pending: %d %+v", code, p)
	}
	code, p, _ := f.poll(t, waiting.DeviceCode)
	if code != http.StatusBadRequest || p.Code != "device.slow_down" || p.Params == nil || (*p.Params)["interval"] != float64(10) {
		t.Fatalf("too fast: %d %+v", code, p)
	}
	if code, p, _ := f.poll(t, "<placeholder: unknown device code>"); code != http.StatusBadRequest || p.Code != "device.expired" {
		t.Errorf("unknown device code: %d %+v", code, p)
	}

	// The confirmation page.
	dc := f.deviceCode(t)
	path := "/api/v1/device/requests/" + dc.UserCode
	if rec, p := f.do(t, http.MethodGet, path, "", "", nil); rec.Code != http.StatusUnauthorized || p.Code != "auth.required" {
		t.Errorf("signed out: %d %+v", rec.Code, p)
	}
	if rec, p := f.do(t, http.MethodPost, path+"/approve", "", "", bearer(f.deviceToken)); rec.Code != http.StatusForbidden || p.Code != "access.denied" {
		t.Errorf("approve with a device token: %d %+v", rec.Code, p)
	}
	rec, _ := f.do(t, http.MethodGet, path, testMember, "", nil)
	var req httpapi.DeviceRequest
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &req) != nil || req.Status != "pending" || req.Name != "dev-laptop" || req.Os != "linux" {
		t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
	if rec, p := f.do(t, http.MethodGet, "/api/v1/device/requests/AAAA-AAAA", testMember, "", nil); rec.Code != http.StatusNotFound || p.Code != "object.not_found" {
		t.Errorf("unknown user code: %d %+v", rec.Code, p)
	}
	if rec, p := f.do(t, http.MethodPost, path+"/approve", testMember, "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("approve: %d %+v", rec.Code, p)
	}
	if rec, p := f.do(t, http.MethodPost, path+"/deny", testMember, "", nil); rec.Code != http.StatusConflict || p.Code != "device.decided" {
		t.Errorf("deny after the approval: %d %+v", rec.Code, p)
	}

	code, p, tok := f.poll(t, dc.DeviceCode)
	if code != http.StatusOK || !strings.HasPrefix(tok.DeviceToken, authn.DeviceTokenPrefix) || tok.User.Id != testMember || tok.User.Username != "jdoe" {
		t.Fatalf("token: %d %+v %+v", code, p, tok)
	}

	// The token signs in as that computer.
	rec, _ = f.do(t, http.MethodGet, "/api/v1/me/devices", "", "", bearer(tok.DeviceToken))
	var list httpapi.DeviceList
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Items) != 1 ||
		list.Items[0].Id != tok.DeviceId || !list.Items[0].Current || list.Items[0].KeyStorage != "file" || list.Items[0].Tools == nil {
		t.Fatalf("my devices with the token: %d %s", rec.Code, rec.Body)
	}
	rec, _ = f.do(t, http.MethodGet, "/api/v1/me/devices", testMember, "", nil)
	if json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Items) != 1 || list.Items[0].Current {
		t.Errorf("my devices in the browser: %s", rec.Body)
	}
	// Someone else cannot sign it out; its owner's computer can (rgd logout).
	if rec, p := f.do(t, http.MethodDelete, "/api/v1/me/devices/"+tok.DeviceId, testAdmin, "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("someone else's device: %d %+v", rec.Code, p)
	}
	if rec, p := f.do(t, http.MethodDelete, "/api/v1/me/devices/"+tok.DeviceId, "", "", bearer(tok.DeviceToken)); rec.Code != http.StatusNoContent {
		t.Fatalf("sign out: %d %+v", rec.Code, p)
	}
	if rec, p := f.do(t, http.MethodGet, "/api/v1/me/devices", "", "", bearer(tok.DeviceToken)); rec.Code != http.StatusUnauthorized || p.Code != "auth.required" {
		t.Errorf("the token after signing out: %d %+v", rec.Code, p)
	}

	// The audit log names the device.
	rec, _ = f.do(t, http.MethodGet, "/api/v1/audit?category=login", testAdmin, "", nil)
	if !strings.Contains(rec.Body.String(), `"name":"dev-laptop"`) {
		t.Errorf("the audit log does not name the device: %s", rec.Body)
	}
}

// Starting rgd login is limited per client address like signing in.
func TestDeviceCodeIsRateLimited(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{realDevices: true})
	for i := 0; i < 10; i++ {
		f.deviceCode(t)
	}
	body := `{"name":"dev-laptop","os":"linux","arch":"amd64","cli_version":"0.5.0","public_key":"` + devicePublicKey(t) + `","key_storage":"file"}`
	rec, p := f.do(t, http.MethodPost, "/api/v1/device/code", "", body, nil)
	if rec.Code != http.StatusTooManyRequests || p.Code != "rate.limited" || rec.Header().Get("Retry-After") == "" {
		t.Errorf("the 11th request: %d %+v", rec.Code, p)
	}
}
