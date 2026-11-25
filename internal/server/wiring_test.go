// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	extnotify "github.com/code2rich/rigdepot/ext/notify"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/events"
	"github.com/code2rich/rigdepot/internal/server/notify"
)

// TestAccessEventsReachTheBus: access publishes on the bus serve makes, after
// its own commit, so that notify and the modules hear of its changes.
func TestAccessEventsReachTheBus(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{background: true})
	var mu sync.Mutex
	var got []string
	f.app.bg.Events.Subscribe("test", nil, func(_ context.Context, ev events.Event) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev.Name)
		return nil
	})
	ctx := context.Background()
	admin := access.Actor{UserID: testAdmin, Origin: access.OriginWeb}
	if _, err := f.app.access.CreateProduct(ctx, admin, access.NewProduct{Code: "shop", Name: "Shop", Owners: []string{testMember}}); err != nil {
		t.Fatal(err)
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := f.app.bg.Events.Drain(dctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(got, access.ProductCreated{}.EventName()) {
		t.Errorf("events on the bus: %v", got)
	}
}

// fakeMail is an e-mail channel that records what it was asked to send.
type fakeMail struct {
	mu   sync.Mutex
	cfgs []extnotify.Config
	msgs []extnotify.Message
	err  error
}

func (*fakeMail) ID() string { return "email" }
func (*fakeMail) ConfigSchema() []byte {
	return []byte(`{"type":"object","properties":{"password":{"type":"string","writeOnly":true}}}`)
}
func (m *fakeMail) Send(_ context.Context, cfg extnotify.Config, msg extnotify.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfgs, m.msgs = append(m.cfgs, cfg), append(m.msgs, msg)
	return m.err
}

func (m *fakeMail) lookup(id string) (extnotify.Channel, bool) { return m, id == "email" }

func readyChecks(t *testing.T, f *apiFixture) map[string]string {
	t.Helper()
	rec, _ := f.do(t, http.MethodGet, "/readyz", "", "", nil)
	var st readyStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("/readyz: %v\n%s", err, rec.Body)
	}
	return st.Checks
}

// TestMailSettingsReachTheOutbox: the outbox and the test mail use the mail server saved in the
// platform settings; until it is configured, /readyz reports mail as degraded.
func TestMailSettingsReachTheOutbox(t *testing.T) {
	ch := &fakeMail{}
	f := newAPIWith(t, fixtureOptions{background: true, channels: ch.lookup})
	if got := readyChecks(t, f)["mail"]; got != "degraded" {
		t.Errorf("/readyz mail before it is configured: %q", got)
	}
	if err := f.app.bg.Notify.Check(context.Background()); !errors.Is(err, notify.ErrNotConfigured) {
		t.Errorf("Check before it is configured: %v", err)
	}

	// The test mail works before mail is turned on, with what is saved.
	const pw = "Zq8-mail-password-marker-4Kd9"
	rec, _ := f.do(t, http.MethodGet, "/api/v1/settings", testAdmin, "", nil)
	change := `{"mail":{"enabled":false,"host":"smtp.example.com","port":465,"security":"tls","username":"rigdepot@example.com","password":"` + pw + `","from":"rigdepot@example.com"}}`
	rec, _ = f.do(t, http.MethodPatch, "/api/v1/settings", testAdmin, change, map[string]string{"If-Match": rec.Header().Get("ETag")})
	if rec.Code != http.StatusOK {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	if rec, p := f.do(t, http.MethodPost, "/api/v1/settings/mail/test", testAdmin, `{"to":"admin@example.com"}`, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("test mail: %d %+v", rec.Code, p)
	}
	ch.mu.Lock()
	if len(ch.cfgs) != 1 || ch.cfgs[0].String("host") != "smtp.example.com" || ch.cfgs[0].Int("port") != 465 ||
		ch.cfgs[0].String("security") != "tls" || ch.cfgs[0].String("password") != pw || ch.cfgs[0].String("from") != "rigdepot@example.com" ||
		ch.msgs[0].To.Address != "admin@example.com" || ch.msgs[0].Kind != notify.TemplateTest {
		t.Errorf("the channel got %v %+v", ch.cfgs, ch.msgs)
	}
	ch.err = errors.New("535 authentication failed with " + pw)
	ch.mu.Unlock()
	rec, p := f.do(t, http.MethodPost, "/api/v1/settings/mail/test", testAdmin, `{"to":"admin@example.com"}`, nil)
	if rec.Code != http.StatusBadGateway || p.Code != "mail.failed" || strings.Contains(rec.Body.String(), pw) || !strings.Contains(*p.Detail, "535") {
		t.Errorf("failed test mail: %d %s", rec.Code, rec.Body)
	}
	// Turned off, the outbox does not use it.
	if err := f.app.bg.Notify.Check(context.Background()); !errors.Is(err, notify.ErrNotConfigured) {
		t.Errorf("Check while mail is off: %v", err)
	}

	rec, _ = f.do(t, http.MethodGet, "/api/v1/settings", testAdmin, "", nil)
	rec, _ = f.do(t, http.MethodPatch, "/api/v1/settings", testAdmin, `{"mail":{"enabled":true}}`, map[string]string{"If-Match": rec.Header().Get("ETag")})
	if rec.Code != http.StatusOK {
		t.Fatalf("turn on: %d %s", rec.Code, rec.Body)
	}
	if got := readyChecks(t, f)["mail"]; got != "ok" {
		t.Errorf("/readyz mail once configured: %q", got)
	}
}
