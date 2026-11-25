// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package notify

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	extnotify "github.com/code2rich/rigdepot/ext/notify"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/events"
	"github.com/code2rich/rigdepot/internal/server/jobs"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/testenv"
	"github.com/code2rich/rigdepot/locales"
)

func TestMain(m *testing.M) { testenv.Main(m) }

const password = "p@ss word-123"

// fakeChannel records what it is asked to send. fail > 0 fails that many attempts, fail < 0
// fails every one; the error quotes the password the way a careless server does.
type fakeChannel struct {
	mu   sync.Mutex
	fail int
	sent []extnotify.Message
	cfgs []extnotify.Config
}

func (*fakeChannel) ID() string { return EmailChannel }
func (*fakeChannel) ConfigSchema() []byte {
	return []byte(`{"type":"object","properties":{"host":{"type":"string"},"password":{"type":"string","writeOnly":true}}}`)
}
func (c *fakeChannel) Send(_ context.Context, cfg extnotify.Config, m extnotify.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail != 0 {
		if c.fail > 0 {
			c.fail--
		}
		return errors.New("535 login failed for " + cfg.String("password") + " (" + base64.StdEncoding.EncodeToString([]byte(cfg.String("password"))) + ")")
	}
	c.sent = append(c.sent, m)
	c.cfgs = append(c.cfgs, cfg)
	return nil
}

func (c *fakeChannel) setFail(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail = n
}

func (c *fakeChannel) messages() []extnotify.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]extnotify.Message(nil), c.sent...)
}

// settings is a ConfigSource; configured false means mail is not set up.
type settings struct {
	mu         sync.Mutex
	configured bool
}

func (s *settings) ChannelConfig(_ context.Context, channel string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if channel != EmailChannel || !s.configured {
		return nil, false, nil
	}
	return []byte(`{"host":"smtp.example.com","password":"` + password + `"}`), true, nil
}

type fixture struct {
	t   *testing.T
	db  *dbx.DB
	svc *Service
	ch  *fakeChannel
	cfg *settings
	now time.Time
}

func catalog(t *testing.T) *i18n.Catalog {
	t.Helper()
	cat, err := i18n.Load(locales.FS, "server")
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func newFixture(t *testing.T, db *dbx.DB, change func(*Options)) *fixture {
	t.Helper()
	ctx := context.Background()
	if _, err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE things (id TEXT NOT NULL PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: db, ch: &fakeChannel{}, cfg: &settings{configured: true}, now: time.Now().UTC()}
	o := Options{
		DB: db, Config: f.cfg, Catalog: catalog(t), DefaultLocale: "en",
		Channels: func(id string) (extnotify.Channel, bool) { return f.ch, id == EmailChannel },
		Now:      func() time.Time { return f.now },
	}
	if change != nil {
		change(&o)
	}
	svc, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	return f
}

// queue writes a business row and queues mails in the same transaction, as a rule would.
func (f *fixture) queue(mails ...Mail) {
	f.t.Helper()
	err := f.db.InTx(context.Background(), func(tx *dbx.Tx) error {
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO things (id) VALUES (?)`, dbx.NewID()); err != nil {
			return err
		}
		return f.svc.Enqueue(context.Background(), tx, mails...)
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) run() {
	f.t.Helper()
	if err := f.svc.RunOnce(context.Background()); err != nil {
		f.t.Fatalf("RunOnce: %v", err)
	}
}

func (f *fixture) stats() Stats {
	f.t.Helper()
	st, err := f.svc.Stats(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func (f *fixture) lastErrors() []string {
	f.t.Helper()
	rows, err := f.db.QueryContext(context.Background(), `SELECT last_error FROM mail_outbox ORDER BY created_at`)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func testMail(to string) Mail {
	return Mail{To: to, Name: "Member", Locale: "en", Template: TemplateTest, Link: "https://depot.example.com/settings"}
}

// TestFailingMailDoesNotAffectTheChange: the change commits whatever the mail server does, and
// the mail stays queued for another attempt.
func TestFailingMailDoesNotAffectTheChange(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := newFixture(t, db, nil)
		f.ch.setFail(-1)
		f.queue(testMail("member@example.com"))
		f.run()
		var things int
		if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM things`).Scan(&things); err != nil || things != 1 {
			t.Errorf("the change did not commit: %d rows, %v", things, err)
		}
		if st := f.stats(); st.Queued != 1 || st.Retrying != 1 || st.Failed != 0 {
			t.Errorf("after a failed attempt: %+v; want it queued for a retry", st)
		}
		if err := f.svc.Check(context.Background()); !errors.Is(err, ErrRetrying) {
			t.Errorf("Check = %v, want ErrRetrying", err)
		}
	})
}

// TestBackoff: 1 minute after the first failure, 2 after the second; nothing in between.
func TestBackoff(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := newFixture(t, db, nil)
		f.ch.setFail(2)
		f.queue(testMail("member@example.com"))
		f.run() // first failure: next in 1 minute
		f.now = f.now.Add(30 * time.Second)
		f.run() // not due
		if n := len(f.ch.messages()); n != 0 {
			t.Fatalf("sent %d", n)
		}
		f.now = f.now.Add(31 * time.Second)
		f.run() // second failure: next in 2 minutes
		f.now = f.now.Add(119 * time.Second)
		f.run() // not due
		if st := f.stats(); st.Queued != 1 || st.Retrying != 1 {
			t.Fatalf("during the backoff: %+v", st)
		}
		f.now = f.now.Add(2 * time.Second)
		f.run() // third attempt succeeds
		if n := len(f.ch.messages()); n != 1 {
			t.Fatalf("after the backoff: %d sent, want 1", n)
		}
		if st := f.stats(); st.Queued != 0 || st.Failed != 0 || st.SentLastHour != 1 {
			t.Errorf("after sending: %+v", st)
		}
		if err := f.svc.Check(context.Background()); err != nil {
			t.Errorf("Check after sending = %v", err)
		}
	})
}

func TestRetryDelay(t *testing.T) {
	for n, want := range map[int]time.Duration{1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 7: time.Hour, 50: time.Hour} {
		if got := retryDelay(n); got != want {
			t.Errorf("retryDelay(%d) = %v, want %v", n, got, want)
		}
	}
}

// TestGivesUpAndRequeues: after the retry window a mail is marked failed, readiness says so, and
// RetryFailed sends it once the server works again.
func TestGivesUpAndRequeues(t *testing.T) {
	f := newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)), nil)
	f.ch.setFail(-1)
	f.queue(testMail("member@example.com"))
	start := f.now
	for f.now.Sub(start) < 25*time.Hour {
		f.run()
		f.now = f.now.Add(time.Hour)
	}
	if st := f.stats(); st.Failed != 1 || st.Queued != 0 {
		t.Fatalf("after 25 hours: %+v; want failed", st)
	}
	if err := f.svc.Check(context.Background()); !errors.Is(err, ErrFailing) {
		t.Errorf("Check = %v, want ErrFailing", err)
	}
	f.ch.setFail(0)
	if n, err := f.svc.RetryFailed(context.Background()); n != 1 || err != nil {
		t.Fatalf("RetryFailed = %d, %v", n, err)
	}
	f.run()
	if n := len(f.ch.messages()); n != 1 || f.stats().Failed != 0 {
		t.Errorf("after requeueing: %d sent, %+v", n, f.stats())
	}
}

// TestPasswordIsMasked: the channel's error quotes the password in clear and in base64; neither
// reaches the outbox nor the test-mail error.
func TestPasswordIsMasked(t *testing.T) {
	f := newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)), nil)
	f.ch.setFail(-1)
	f.queue(testMail("member@example.com"))
	f.run()
	b64 := base64.StdEncoding.EncodeToString([]byte(password))
	for _, e := range f.lastErrors() {
		if strings.Contains(e, password) || strings.Contains(e, b64) || strings.Contains(e, "word-123") {
			t.Errorf("the stored error has the password: %q", e)
		}
		if !strings.Contains(e, "535") || !strings.Contains(e, "***") {
			t.Errorf("the stored error lost what went wrong: %q", e)
		}
	}
	err := f.svc.SendTest(context.Background(), "admin@example.com", "en")
	if err == nil || strings.Contains(err.Error(), password) || strings.Contains(err.Error(), b64) {
		t.Errorf("SendTest error: %v", err)
	}
}

// TestRendersInTheRecipientsLanguage: each mail is in its recipient's language; a recipient
// without one gets the server's default.
func TestRendersInTheRecipientsLanguage(t *testing.T) {
	f := newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)), func(o *Options) { o.DefaultLocale = "zh-CN" })
	zh, en, none := testMail("zh@example.com"), testMail("en@example.com"), testMail("none@example.com")
	zh.Locale, en.Locale, none.Locale = "zh-CN", "en-GB", ""
	f.queue(zh, en, none)
	f.run()
	got := map[string]extnotify.Message{}
	for _, m := range f.ch.messages() {
		got[m.To.Address] = m
	}
	want := map[string]struct{ subject, body, link, locale string }{
		"zh@example.com":   {brand.Name + " 的测试邮件", "说明 " + brand.Name + " 用现在的设置可以发信", "详情：https://depot.example.com/settings", "zh-CN"},
		"en@example.com":   {"Test message from " + brand.Name, "If you can read it, " + brand.Name + " can send mail", "Details: https://depot.example.com/settings", "en"},
		"none@example.com": {brand.Name + " 的测试邮件", "说明 " + brand.Name, "详情：", "zh-CN"},
	}
	for addr, w := range want {
		m, ok := got[addr]
		if !ok {
			t.Errorf("nothing sent to %s", addr)
			continue
		}
		if m.Subject != w.subject || !strings.Contains(m.Text, w.body) || !strings.Contains(m.Text, w.link) || m.Locale != w.locale {
			t.Errorf("%s: subject %q locale %q text %q", addr, m.Subject, m.Locale, m.Text)
		}
		if !strings.Contains(m.HTML, `href="https://depot.example.com/settings"`) || m.From != brand.Name || m.Kind != TemplateTest {
			t.Errorf("%s: html %q from %q kind %q", addr, m.HTML, m.From, m.Kind)
		}
	}
}

// TestParametersAreEscapedInHTML: a parameter cannot inject markup.
func TestParametersAreEscapedInHTML(t *testing.T) {
	r := renderer{cat: catalog(t), defaultLocale: "en"}
	out := r.digest("en", 3, []digestItem{{subject: `<script>alert(1)</script>`, link: `https://depot.example.com/"><b>x`}})
	if strings.Contains(out.html, "<script>") || strings.Contains(out.html, `"><b>`) {
		t.Errorf("markup got through: %s", out.html)
	}
}

// TestNotConfiguredWaits: without mail settings nothing is attempted and nothing fails; once
// configured, the waiting mail goes out.
func TestNotConfiguredWaits(t *testing.T) {
	f := newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)), nil)
	f.cfg.configured = false
	f.queue(testMail("member@example.com"))
	for range 3 {
		f.run()
		f.now = f.now.Add(10 * time.Hour)
	}
	if st := f.stats(); st.Queued != 1 || st.Retrying != 0 || st.Failed != 0 || len(f.ch.messages()) != 0 {
		t.Fatalf("not configured: %+v, %d sent", st, len(f.ch.messages()))
	}
	if err := f.svc.Check(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Check = %v, want ErrNotConfigured", err)
	}
	if err := f.svc.SendTest(context.Background(), "admin@example.com", "en"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("SendTest = %v, want ErrNotConfigured", err)
	}
	f.cfg.configured = true
	f.run()
	if n := len(f.ch.messages()); n != 1 {
		t.Errorf("once configured: %d sent", n)
	}
}

// TestHourlyLimitMergesIntoDigest: with a limit of 3, the fourth and later mails to one person
// go out as one digest; another person is not affected; when the hour is used up, the rest
// waits for the next one.
func TestHourlyLimitMergesIntoDigest(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := newFixture(t, db, func(o *Options) { o.HourlyLimit = 3 })
		var mails []Mail
		for range 6 {
			mails = append(mails, testMail("busy@example.com"))
		}
		f.queue(append(mails, testMail("other@example.com"))...)
		f.run()
		var busy, other []extnotify.Message
		for _, m := range f.ch.messages() {
			switch m.To.Address {
			case "busy@example.com":
				busy = append(busy, m)
			case "other@example.com":
				other = append(other, m)
			}
		}
		if len(busy) != 3 || len(other) != 1 {
			t.Fatalf("sent %d to the busy recipient and %d to the other; want 3 and 1", len(busy), len(other))
		}
		digest := busy[2]
		if digest.Kind != "digest" || digest.Subject != "4 more notifications" || strings.Count(digest.Text, "• ") != 4 || !strings.Contains(digest.Text, "limit of 3") {
			t.Errorf("digest: kind %q subject %q text %q", digest.Kind, digest.Subject, digest.Text)
		}
		if st := f.stats(); st.Queued != 0 || st.SentLastHour != 4 {
			t.Errorf("after the run: %+v; want all sent as 4 messages", st)
		}

		f.queue(testMail("busy@example.com"))
		f.run()
		if n := len(f.ch.messages()); n != 4 {
			t.Errorf("the limit was exceeded: %d messages", n)
		}
		f.now = f.now.Add(time.Hour + time.Second)
		f.run()
		if n := len(f.ch.messages()); n != 5 {
			t.Errorf("the next hour: %d messages, want 5", n)
		}
	})
}

// TestEventToMail: a rule on an event queues mail once the change commits, and the outbox job
// sends it right away instead of waiting for its interval.
func TestEventToMail(t *testing.T) {
	db := dbxtest.Open(t, dbxtest.SQLiteURL(t))
	sched := jobs.New(db, jobs.Options{})
	f := newFixture(t, db, func(o *Options) { o.Jobs = sched; o.Now = nil })
	bus := events.New(nil)
	t.Cleanup(func() {
		_ = bus.Close(context.Background())
		_ = sched.Stop(context.Background())
	})
	f.svc.On(bus, []string{"thing.done"}, func(_ context.Context, ev events.Event) ([]Mail, error) {
		return []Mail{testMail(ev.Payload.(thingDone).Owner)}, nil
	})
	sched.Start()
	// The first run of the job happens at start; wait for it so that only the trigger can send.
	time.Sleep(200 * time.Millisecond)

	_ = db.InTx(context.Background(), func(tx *dbx.Tx) error {
		bus.Publish(tx, thingDone{Owner: "rolled-back@example.com"})
		return errors.New("boom")
	})
	if err := db.InTx(context.Background(), func(tx *dbx.Tx) error {
		bus.Publish(tx, thingDone{Owner: "owner@example.com"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(f.ch.messages()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := f.ch.messages()
	if len(got) != 1 || got[0].To.Address != "owner@example.com" {
		t.Errorf("sent %+v; want one mail to the owner within seconds", got)
	}
}

type thingDone struct{ Owner string }

func (thingDone) EventName() string { return "thing.done" }

func TestEnqueueChecks(t *testing.T) {
	f := newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)), nil)
	bad := testMail("a@example.com")
	bad.Template = "no-such-template"
	err := f.db.InTx(context.Background(), func(tx *dbx.Tx) error { return f.svc.Enqueue(context.Background(), tx, bad) })
	if err == nil {
		t.Error("an unknown template was queued")
	}
	f.queue(testMail("a@example.com, b@example.com"), testMail("Eve <e@example.com>"), testMail("ok@example.com"))
	if st := f.stats(); st.Queued != 1 {
		t.Errorf("queued %d, want only the one valid address", st.Queued)
	}
}
