// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/authn"
)

// deviceEnv is env with the real device code flow behind the service.
type deviceEnv struct {
	*env
	devices *authn.Devices
}

func newDeviceEnv(t *testing.T) *deviceEnv {
	t.Helper()
	e := newEnv(t, nil)
	d := authn.NewDevices(e.db, authn.DevicesOptions{Access: e.access, Audit: e.audit, Now: e.clock.now})
	e.svc = authn.New(e.db, authn.Options{Access: e.access, Audit: e.audit, Devices: d, Hash: cheap, Now: e.clock.now})
	return &deviceEnv{env: e, devices: d}
}

// browser signs name in and returns the caller of their browser session.
func (e *deviceEnv) browser(t *testing.T, name, password string) authn.Caller {
	t.Helper()
	in := e.login(t, name, password)
	c, err := e.svc.Session(context.Background(), in.Token)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func publicKey(t *testing.T) string {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
}

func laptop(t *testing.T) authn.DeviceInfo {
	return authn.DeviceInfo{Name: "dev-laptop", OS: "darwin", Arch: "arm64", CLIVersion: "0.5.0", PublicKey: publicKey(t), KeyStorage: authn.KeyStorageKeyring}
}

// start asks for a device code for a laptop.
func (e *deviceEnv) start(t *testing.T) authn.DeviceCode {
	t.Helper()
	code, err := e.devices.StartDeviceLogin(context.Background(), laptop(t))
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// The device code sequence (docs/architecture/06-server.md#rgd-login设备码流程),
// step by step: the request with the public key, waiting, the approval in
// the browser, the token at the next poll, and the token signing in as
// that computer.
func TestDeviceCodeSequence(t *testing.T) {
	e := newDeviceEnv(t)
	ctx := context.Background()
	info := laptop(t)

	// Step: the command line asks, with its computer and public key.
	code, err := e.devices.StartDeviceLogin(ctx, info)
	if err != nil {
		t.Fatal(err)
	}
	if len(code.UserCode) != 8 || strings.ContainsAny(code.UserCode, "0O1IL") || code.Interval != 5*time.Second || code.ExpiresIn != 10*time.Minute {
		t.Fatalf("device code: %+v", code)
	}
	if len(code.DeviceCode) < 40 || strings.Contains(code.DeviceCode, code.UserCode) {
		t.Fatalf("device code %q is not a long random secret apart from the user code", code.DeviceCode)
	}
	var n int
	e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM device_codes WHERE device_code_digest = ?`, code.DeviceCode).Scan(&n)
	if n != 0 {
		t.Error("the device code is stored in plain")
	}

	// Step: polling before anyone decided.
	if _, err := e.devices.PollDevice(ctx, code.DeviceCode); !errors.Is(err, authn.ErrAuthorizationPending) {
		t.Fatalf("first poll: %v", err)
	}

	// Step: the browser shows the request; the code is accepted as typed.
	admin := e.browser(t, "admin", adminPassword)
	typed := strings.ToLower(code.UserCode[:4] + " " + code.UserCode[4:])
	req, err := e.devices.DeviceRequest(ctx, admin, typed)
	if err != nil {
		t.Fatal(err)
	}
	if req.UserCode != authn.ShowUserCode(code.UserCode) || req.Name != "dev-laptop" || req.OS != "darwin" || req.Arch != "arm64" ||
		req.CLIVersion != "0.5.0" || req.Status != authn.DeviceRequestPending || req.ExpiresAt.Sub(req.RequestedAt) != 10*time.Minute {
		t.Fatalf("request: %+v", req)
	}

	// Step: approved in the browser; audited as the command line's sign-in.
	if err := e.devices.ApproveDevice(ctx, admin, authn.ShowUserCode(code.UserCode)); err != nil {
		t.Fatal(err)
	}
	rows := e.entries(t, audit.Filter{Category: audit.CategoryLogin, Action: authn.ActionLogin})
	if len(rows) == 0 || rows[0].Method != authn.MethodDeviceCode || rows[0].Actor.Origin != audit.OriginCLI ||
		rows[0].Actor.UserID != e.admin.ID || rows[0].Actor.DeviceID == "" || rows[0].Object.Kind != "device" || rows[0].Object.Label != "dev-laptop" {
		t.Fatalf("sign-in entry: %+v", rows)
	}
	// Not signed in until the command line has its token.
	if ds, err := e.devices.MyDevices(ctx, admin); err != nil || len(ds) != 0 {
		t.Fatalf("devices before the token is collected: %+v %v", ds, err)
	}

	// Step: the next poll (after the interval) gets the token, once.
	e.clock.advance(5 * time.Second)
	issued, err := e.devices.PollDevice(ctx, code.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	if !authn.TokenWellFormed(authn.DeviceTokenPrefix, issued.Token) || issued.DeviceID != rows[0].Actor.DeviceID || issued.User.Username != "admin" {
		t.Fatalf("issued: %+v", issued)
	}
	e.clock.advance(5 * time.Second)
	if _, err := e.devices.PollDevice(ctx, code.DeviceCode); !errors.Is(err, authn.ErrDeviceExpired) {
		t.Fatalf("a used code: %v", err)
	}
	e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE token_digest = ?`, issued.Token).Scan(&n)
	if n != 0 {
		t.Error("the device token is stored in plain")
	}

	// Step: the token signs in as that computer.
	c, err := e.svc.Device(ctx, issued.Token)
	if err != nil || c.Actor.UserID != e.admin.ID || c.Actor.DeviceID != issued.DeviceID || c.Actor.Origin != access.OriginCLI || c.SessionID != "" {
		t.Fatalf("device caller: %+v %v", c, err)
	}
	ds, err := e.devices.MyDevices(ctx, c)
	if err != nil || len(ds) != 1 || !ds[0].Current || ds[0].Name != "dev-laptop" || ds[0].KeyStorage != "keyring" || ds[0].PublicKey != info.PublicKey {
		t.Fatalf("my devices: %+v %v", ds, err)
	}
	if name, ok := e.devices.DeviceName(ctx, issued.DeviceID); !ok || name != "dev-laptop" {
		t.Errorf("device name: %q %v", name, ok)
	}
	if req, _ := e.devices.DeviceRequest(ctx, admin, code.UserCode); req.Status != authn.DeviceRequestApproved {
		t.Errorf("request after the approval: %+v", req)
	}
}

// The acceptance check "an expired user code": after ten minutes the
// command line gets device.expired and the browser can no longer approve.
func TestDeviceCodeExpires(t *testing.T) {
	e := newDeviceEnv(t)
	ctx := context.Background()
	code := e.start(t)
	admin := e.browser(t, "admin", adminPassword)
	e.clock.advance(10*time.Minute - time.Millisecond)
	if _, err := e.devices.PollDevice(ctx, code.DeviceCode); !errors.Is(err, authn.ErrAuthorizationPending) {
		t.Fatalf("just before it expires: %v", err)
	}
	e.clock.advance(time.Millisecond + 5*time.Second)
	if _, err := e.devices.PollDevice(ctx, code.DeviceCode); !errors.Is(err, authn.ErrDeviceExpired) || kindOf(err) != authn.KindDeviceFlow {
		t.Fatalf("expired poll: %v", err)
	}
	if req, err := e.devices.DeviceRequest(ctx, admin, code.UserCode); err != nil || req.Status != authn.DeviceRequestExpired {
		t.Fatalf("expired request: %+v %v", req, err)
	}
	if err := e.devices.ApproveDevice(ctx, admin, code.UserCode); !errors.Is(err, authn.ErrDeviceRequestExpired) || kindOf(err) != authn.KindConflict {
		t.Fatalf("approving an expired request: %v", err)
	}
	if err := e.devices.DenyDevice(ctx, admin, code.UserCode); !errors.Is(err, authn.ErrDeviceRequestExpired) {
		t.Fatalf("refusing an expired request: %v", err)
	}
	// An approval that came in time but was never collected expires too.
	code = e.start(t)
	if err := e.devices.ApproveDevice(ctx, admin, code.UserCode); err != nil {
		t.Fatal(err)
	}
	e.clock.advance(10 * time.Minute)
	if _, err := e.devices.PollDevice(ctx, code.DeviceCode); !errors.Is(err, authn.ErrDeviceExpired) {
		t.Fatalf("collecting after the expiry: %v", err)
	}
	// Unknown device codes are expired ones.
	for _, bad := range []string{"", "unknown", strings.Repeat("x", 300)} {
		if _, err := e.devices.PollDevice(ctx, bad); !errors.Is(err, authn.ErrDeviceExpired) {
			t.Errorf("poll %q: %v", bad, err)
		}
	}
}

// The acceptance check "refused": the command line gets device.denied, and
// the request cannot be approved afterwards.
func TestDeviceRequestDenied(t *testing.T) {
	e := newDeviceEnv(t)
	ctx := context.Background()
	code := e.start(t)
	admin := e.browser(t, "admin", adminPassword)
	if err := e.devices.DenyDevice(ctx, admin, code.UserCode); err != nil {
		t.Fatal(err)
	}
	if _, err := e.devices.PollDevice(ctx, code.DeviceCode); !errors.Is(err, authn.ErrDeviceDenied) || kindOf(err) != authn.KindDeviceFlow {
		t.Fatalf("poll after the refusal: %v", err)
	}
	if err := e.devices.ApproveDevice(ctx, admin, code.UserCode); !errors.Is(err, authn.ErrDeviceRequestDecided) {
		t.Fatalf("approving a refused request: %v", err)
	}
	if req, _ := e.devices.DeviceRequest(ctx, admin, code.UserCode); req.Status != authn.DeviceRequestDenied {
		t.Errorf("request: %+v", req)
	}
	rows := e.entries(t, audit.Filter{Action: authn.ActionDeviceDeny})
	if len(rows) != 1 || rows[0].Actor.UserID != e.admin.ID || rows[0].Object.Label != "dev-laptop" || rows[0].Category != audit.CategoryLogin {
		t.Errorf("refusal entries: %+v", rows)
	}
	var devices int
	e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices`).Scan(&devices)
	if devices != 0 {
		t.Errorf("%d devices after a refusal", devices)
	}
}

// The acceptance check "polling too fast": slow_down, and the interval
// grows by five seconds for this and every later poll (RFC 8628, 3.5).
func TestDevicePollTooFast(t *testing.T) {
	e := newDeviceEnv(t)
	ctx := context.Background()
	code := e.start(t)
	interval := func(err error) any {
		var ae *authn.Error
		if errors.As(err, &ae) {
			return ae.Params["interval"]
		}
		return nil
	}
	if _, err := e.devices.PollDevice(ctx, code.DeviceCode); !errors.Is(err, authn.ErrAuthorizationPending) {
		t.Fatal(err)
	}
	e.clock.advance(4 * time.Second)
	_, err := e.devices.PollDevice(ctx, code.DeviceCode)
	if !errors.Is(err, authn.ErrSlowDown) || kindOf(err) != authn.KindDeviceFlow || interval(err) != 10 {
		t.Fatalf("after 4s: %v (interval %v)", err, interval(err))
	}
	// The old interval is no longer enough.
	e.clock.advance(5 * time.Second)
	if _, err := e.devices.PollDevice(ctx, code.DeviceCode); !errors.Is(err, authn.ErrSlowDown) || interval(err) != 15 {
		t.Fatalf("after 5s more: %v (interval %v)", err, interval(err))
	}
	e.clock.advance(15 * time.Second)
	if _, err := e.devices.PollDevice(ctx, code.DeviceCode); !errors.Is(err, authn.ErrAuthorizationPending) {
		t.Fatalf("after the new interval: %v", err)
	}
	// A slow_down for one request does not affect another.
	other := e.start(t)
	if _, err := e.devices.PollDevice(ctx, other.DeviceCode); !errors.Is(err, authn.ErrAuthorizationPending) {
		t.Fatalf("another request: %v", err)
	}
}

// The acceptance check "a device signed out loses its token at once", for
// each way to sign it out.
func TestSignedOutDeviceTokenStopsWorking(t *testing.T) {
	ctx := context.Background()
	login := func(e *deviceEnv, browser authn.Caller) authn.IssuedDevice {
		t.Helper()
		code := e.start(t)
		if err := e.devices.ApproveDevice(ctx, browser, code.UserCode); err != nil {
			t.Fatal(err)
		}
		issued, err := e.devices.PollDevice(ctx, code.DeviceCode)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Device(ctx, issued.Token); err != nil {
			t.Fatal(err)
		}
		return issued
	}
	refused := func(e *deviceEnv, token, how string) {
		t.Helper()
		if _, err := e.svc.Device(ctx, token); !errors.Is(err, authn.ErrUnauthenticated) {
			t.Errorf("%s: the token still works: %v", how, err)
		}
	}

	t.Run("from the browser", func(t *testing.T) {
		e := newDeviceEnv(t)
		admin := e.browser(t, "admin", adminPassword)
		issued := login(e, admin)
		keep := login(e, admin)
		if err := e.devices.SignOutDevice(ctx, admin, issued.DeviceID); err != nil {
			t.Fatal(err)
		}
		refused(e, issued.Token, "signed out")
		if _, err := e.svc.Device(ctx, keep.Token); err != nil {
			t.Errorf("the other device: %v", err)
		}
		if err := e.devices.SignOutDevice(ctx, admin, issued.DeviceID); err != nil {
			t.Errorf("signing out again: %v", err)
		}
		if rows := e.entries(t, audit.Filter{Action: authn.ActionDeviceSignOut}); len(rows) != 1 || rows[0].Object.ID != issued.DeviceID {
			t.Errorf("sign-out entries: %+v", rows)
		}
		if ds, _ := e.devices.MyDevices(ctx, admin); len(ds) != 1 || ds[0].ID != keep.DeviceID || ds[0].Current {
			t.Errorf("devices left: %+v", ds)
		}
	})
	t.Run("by itself (rgd logout)", func(t *testing.T) {
		e := newDeviceEnv(t)
		issued := login(e, e.browser(t, "admin", adminPassword))
		self, _ := e.svc.Device(ctx, issued.Token)
		if err := e.devices.SignOutDevice(ctx, self, issued.DeviceID); err != nil {
			t.Fatal(err)
		}
		refused(e, issued.Token, "rgd logout")
	})
	t.Run("not someone else's", func(t *testing.T) {
		e := newDeviceEnv(t)
		e.user(t, "jdoe", "<placeholder: jdoe password>")
		issued := login(e, e.browser(t, "admin", adminPassword))
		jdoe := e.browser(t, "jdoe", "<placeholder: jdoe password>")
		if err := e.devices.SignOutDevice(ctx, jdoe, issued.DeviceID); !errors.Is(err, authn.ErrNotFound) {
			t.Errorf("someone else's device: %v", err)
		}
		if _, err := e.svc.Device(ctx, issued.Token); err != nil {
			t.Errorf("after someone else tried: %v", err)
		}
	})
	t.Run("everywhere", func(t *testing.T) {
		e := newDeviceEnv(t)
		admin := e.browser(t, "admin", adminPassword)
		a, b := login(e, admin), login(e, admin)
		pending := e.start(t)
		if err := e.devices.ApproveDevice(ctx, admin, pending.UserCode); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.SignOutEverywhere(ctx, admin); err != nil {
			t.Fatal(err)
		}
		refused(e, a.Token, "signed out everywhere")
		refused(e, b.Token, "signed out everywhere")
		// An approved request not collected yet cannot be collected now.
		if _, err := e.devices.PollDevice(ctx, pending.DeviceCode); !errors.Is(err, authn.ErrDeviceExpired) {
			t.Errorf("collecting after signing out everywhere: %v", err)
		}
		if rows := e.entries(t, audit.Filter{Action: authn.ActionSignOutAll}); len(rows) != 1 || rows[0].Change != `{"devices":2,"sessions":1}` {
			t.Errorf("sign-out-all entry: %+v", rows)
		}
	})
	t.Run("account disabled", func(t *testing.T) {
		e := newDeviceEnv(t)
		u := e.user(t, "jdoe", "<placeholder: jdoe password>")
		issued := login(e, e.browser(t, "jdoe", "<placeholder: jdoe password>"))
		disabled := access.StatusDisabled
		if _, err := e.access.UpdateUser(ctx, access.Actor{UserID: e.admin.ID, Origin: access.OriginWeb}, u.ID, access.UserChange{Status: &disabled}); err != nil {
			t.Fatal(err)
		}
		refused(e, issued.Token, "account disabled")
	})
	t.Run("idle for 90 days", func(t *testing.T) {
		e := newDeviceEnv(t)
		issued := login(e, e.browser(t, "admin", adminPassword))
		e.clock.advance(authn.DeviceIdle - time.Minute)
		if _, err := e.svc.Device(ctx, issued.Token); err != nil {
			t.Fatalf("a minute short of 90 days: %v", err) // and it counts as activity
		}
		e.clock.advance(authn.DeviceIdle)
		refused(e, issued.Token, "idle for 90 days")
		if err := e.devices.PurgeDevices(ctx); err != nil {
			t.Fatal(err)
		}
		rows := e.entries(t, audit.Filter{Action: authn.ActionDeviceSignOut})
		if len(rows) != 1 || rows[0].Actor.Origin != audit.OriginSystem || !strings.Contains(rows[0].Change, "inactive") {
			t.Errorf("automatic sign-out entries: %+v", rows)
		}
	})
}

// Only a signed-in browser of an activated account decides; a request
// someone else decided is not shown to others.
func TestDeviceRequestsOnlyFromTheBrowser(t *testing.T) {
	e := newDeviceEnv(t)
	ctx := context.Background()
	code := e.start(t)
	admin := e.browser(t, "admin", adminPassword)
	cli := authn.Caller{Actor: access.Actor{UserID: e.admin.ID, Origin: access.OriginCLI, DeviceID: "0193a4b2-6c1e-7a00-8000-000000000201"}}
	token := authn.Caller{Actor: access.Actor{UserID: e.admin.ID, Origin: access.OriginCLI, TokenID: "t", Scope: access.TokenPublish}}
	for _, c := range []authn.Caller{cli, token, {}} {
		if err := e.devices.ApproveDevice(ctx, c, code.UserCode); !errors.Is(err, authn.ErrDenied) {
			t.Errorf("approve as %+v: %v", c.Actor, err)
		}
		if _, err := e.devices.DeviceRequest(ctx, c, code.UserCode); !errors.Is(err, authn.ErrDenied) {
			t.Errorf("look as %+v: %v", c.Actor, err)
		}
	}
	for _, bad := range []string{"", "KQ7M", "KQ7M-2XH0", "0000-0000", "KQ7M-2XHP-X"} {
		if err := e.devices.ApproveDevice(ctx, admin, bad); !errors.Is(err, authn.ErrNotFound) {
			t.Errorf("approve %q: %v", bad, err)
		}
	}
	if err := e.devices.ApproveDevice(ctx, admin, code.UserCode); err != nil {
		t.Fatal(err)
	}
	e.user(t, "jdoe", "<placeholder: jdoe password>")
	jdoe := e.browser(t, "jdoe", "<placeholder: jdoe password>")
	if _, err := e.devices.DeviceRequest(ctx, jdoe, code.UserCode); !errors.Is(err, authn.ErrNotFound) {
		t.Errorf("someone else's approved request: %v", err)
	}
	if err := e.devices.DenyDevice(ctx, jdoe, code.UserCode); !errors.Is(err, authn.ErrNotFound) {
		t.Errorf("refusing someone else's approved request: %v", err)
	}
}

func TestDeviceCodeRequestIsValidated(t *testing.T) {
	e := newDeviceEnv(t)
	ctx := context.Background()
	zero := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, c := range []struct {
		change func(*authn.DeviceInfo)
		want   string
	}{
		{func(i *authn.DeviceInfo) { i.Name = "  " }, "name:required"},
		{func(i *authn.DeviceInfo) { i.Name = strings.Repeat("名", 65) }, "name:too_long"},
		{func(i *authn.DeviceInfo) { i.Name = "a\nb" }, "name:invalid"},
		{func(i *authn.DeviceInfo) { i.OS = "plan9" }, "os:enum"},
		{func(i *authn.DeviceInfo) { i.Arch = "ARM 64" }, "arch:invalid"},
		{func(i *authn.DeviceInfo) { i.CLIVersion = "0.0.0-dev+abc" }, "cli_version:invalid"},
		{func(i *authn.DeviceInfo) { i.PublicKey = "short" }, "public_key:invalid"},
		{func(i *authn.DeviceInfo) { i.PublicKey = zero }, "public_key:invalid"},
		{func(i *authn.DeviceInfo) { i.PublicKey = i.PublicKey + "=" }, "public_key:invalid"},
		{func(i *authn.DeviceInfo) { i.KeyStorage = "keychain" }, "key_storage:enum"},
	} {
		info := laptop(t)
		c.change(&info)
		_, err := e.devices.StartDeviceLogin(ctx, info)
		if got := fieldOf(err); got != c.want || kindOf(err) != authn.KindInvalid {
			t.Errorf("%+v: %v, want %s", info, err, c.want)
		}
	}
	info := laptop(t)
	info.Name = strings.Repeat("名", 64)
	info.KeyStorage = authn.KeyStorageFile
	info.CLIVersion = "0.0.0-dev"
	if _, err := e.devices.StartDeviceLogin(ctx, info); err != nil {
		t.Errorf("64 characters, a file, a development version: %v", err)
	}
}

// The expire job forgets requests a day after they ended, with the devices
// that never collected their token, and keeps the rest.
func TestPurgeDevicesForgetsEndedRequests(t *testing.T) {
	e := newDeviceEnv(t)
	ctx := context.Background()
	admin := e.browser(t, "admin", adminPassword)
	never := e.start(t)
	if err := e.devices.ApproveDevice(ctx, admin, never.UserCode); err != nil {
		t.Fatal(err)
	}
	done := e.start(t)
	if err := e.devices.ApproveDevice(ctx, admin, done.UserCode); err != nil {
		t.Fatal(err)
	}
	issued, err := e.devices.PollDevice(ctx, done.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	count := func(table string) int {
		var n int
		if err := e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	e.clock.advance(time.Hour)
	if err := e.devices.PurgeDevices(ctx); err != nil {
		t.Fatal(err)
	}
	if count("device_codes") != 2 || count("devices") != 2 {
		t.Fatalf("purged too early: %d codes, %d devices", count("device_codes"), count("devices"))
	}
	e.clock.advance(24 * time.Hour)
	if _, err := e.svc.Device(ctx, issued.Token); err != nil { // stays active
		t.Fatal(err)
	}
	if err := e.devices.PurgeDevices(ctx); err != nil {
		t.Fatal(err)
	}
	if count("device_codes") != 0 || count("devices") != 1 {
		t.Fatalf("after a day: %d codes, %d devices", count("device_codes"), count("devices"))
	}
	if _, err := e.svc.Device(ctx, issued.Token); err != nil {
		t.Errorf("the signed-in device after the purge: %v", err)
	}
}
