// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package modhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/code2rich/rigdepot/ext/module"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/events"
	"github.com/code2rich/rigdepot/internal/server/jobs"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/server/modhost"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// fakeModule uses every part of the host: a route, a migration, a job, an event subscription,
// an audited write and its own setting.
type fakeModule struct {
	id       string
	failWith error // Start registers everything, then fails with this

	ticks   atomic.Int32
	stopped atomic.Int32
	mu      sync.Mutex
	events  []module.Event
}

func (m *fakeModule) ID() string { return m.id }
func (m *fakeModule) Describe() module.Info {
	return module.Info{Name: "module." + m.id + ".name", Description: "module." + m.id + ".description"}
}

var fakeMigrations = fstest.MapFS{
	"0001_items.sql": {Data: []byte("CREATE TABLE fake_items (id TEXT NOT NULL PRIMARY KEY, name TEXT NOT NULL);\n")},
}

func (m *fakeModule) Start(ctx context.Context, h module.Host) error {
	if err := h.Migrate(ctx, fakeMigrations); err != nil {
		return err
	}
	db := h.DB()
	r := h.Routes()
	r.Handle("GET /hello/{name}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello "+r.PathValue("name"))
	}))
	r.Handle("POST /items/{name}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := h.Authz().Actor(r.Context())
		if !ok {
			http.Error(w, "who?", http.StatusUnauthorized)
			return
		}
		err := db.InTx(r.Context(), func(tx module.Tx) error {
			id := dbx.NewID()
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO fake_items (id, name) VALUES (?, ?)`, id, r.PathValue("name")); err != nil {
				return err
			}
			return h.Audit().Append(r.Context(), tx, actor, module.AuditEntry{Action: m.id + ".create", ObjectKind: "item", ObjectID: id, ObjectLabel: r.PathValue("name")})
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	if err := h.Jobs().Add(module.Job{Name: "tick", Every: time.Hour, Timeout: time.Minute, Run: func(context.Context) error {
		m.ticks.Add(1)
		return nil
	}}); err != nil {
		return err
	}
	h.Events().Subscribe([]string{"thing.done"}, func(_ context.Context, ev module.Event) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.events = append(m.events, ev)
		return nil
	})
	if err := h.Settings().Set(ctx, "started", json.RawMessage(`true`)); err != nil {
		return err
	}
	return m.failWith
}

func (m *fakeModule) Stop(context.Context) error { m.stopped.Add(1); return nil }

func (m *fakeModule) received() []module.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.events)
}

type thingDone struct{ Name string }

func (thingDone) EventKind() string { return "thing.done" }

// memSettings is a settings store in memory. failGet makes every read fail.
type memSettings struct {
	mu      sync.Mutex
	values  map[string]string
	failGet bool
}

func newSettings(kv ...string) *memSettings {
	s := &memSettings{values: map[string]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		s.values[kv[i]] = kv[i+1]
	}
	return s
}

func (s *memSettings) Get(_ context.Context, key string) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failGet {
		return nil, false, errors.New("database is locked")
	}
	v, ok := s.values[key]
	return json.RawMessage(v), ok, nil
}

func (s *memSettings) Set(_ context.Context, key string, value json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = string(value)
	return nil
}

func (s *memSettings) get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

// memAudit records entries and checks that they arrive inside the module's transaction.
type memAudit struct {
	mu      sync.Mutex
	entries []module.AuditEntry
}

func (a *memAudit) Append(ctx context.Context, tx *dbx.Tx, actor module.Actor, e module.AuditEntry) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM fake_items WHERE id = ?`, e.ObjectID).Scan(&n); err != nil || n != 1 {
		return errors.New("the audit entry is not in the transaction of the change")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	e.Reason = actor.UserID // keep who it was, for the test
	a.entries = append(a.entries, e)
	return nil
}

type ctxUser struct{}

// headerAuthz takes the user from a context value, as the authentication middleware would.
type headerAuthz struct{}

func (headerAuthz) Actor(ctx context.Context) (module.Actor, bool) {
	id, ok := ctx.Value(ctxUser{}).(string)
	return module.Actor{UserID: id, Origin: "web"}, ok
}
func (headerAuthz) Allowed(context.Context, module.Actor, string, module.Target) (bool, error) {
	return true, nil
}

type fixture struct {
	db    *dbx.DB
	bus   *events.Bus
	sched *jobs.Scheduler
	host  *modhost.Host
	srv   http.Handler
	audit *memAudit
}

func newFixture(t *testing.T, db *dbx.DB, st modhost.SettingsStore, locked []string, mods ...module.Module) *fixture {
	t.Helper()
	ctx := context.Background()
	if _, err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, bus: events.New(nil), sched: jobs.New(db, jobs.Options{}), audit: &memAudit{}}
	f.sched.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		f.host.Stop(ctx)
		_ = f.sched.Stop(ctx)
		_ = f.bus.Close(ctx)
	})
	host, err := modhost.New(modhost.Deps{
		DB: db, Events: f.bus, Jobs: f.sched, Audit: f.audit, Authz: headerAuthz{}, Settings: st,
		Locked: locked, Modules: mods,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.host = host
	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "core 404", http.StatusNotFound)
	})
	f.srv = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), ctxUser{}, "user-1"))
		host.Handler(notFound).ServeHTTP(w, r)
	})
	return f
}

func (f *fixture) do(t *testing.T, method, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

func (f *fixture) publish(t *testing.T, name string) {
	t.Helper()
	ctx := context.Background()
	if err := f.db.InTx(ctx, func(tx *dbx.Tx) error { f.bus.Publish(tx, thingDone{name}); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := f.bus.Drain(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func tableExists(t *testing.T, db *dbx.DB, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+name).Scan(&n)
	return err == nil
}

// TestFakeModule turns a module on through the setting and checks each thing the host gives it:
// routes, migrations, jobs, events, audit and settings; then turns it off and on again.
func TestFakeModule(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		ctx := context.Background()
		st := newSettings(modhost.EnabledKey, `["fake"]`)
		m := &fakeModule{id: "fake"}
		f := newFixture(t, db, st, nil, m)
		if err := f.host.Start(ctx); err != nil {
			t.Fatal(err)
		}
		if got := f.host.Enabled(); !slices.Equal(got, []string{"fake"}) {
			t.Fatalf("Enabled() = %v", got)
		}

		// Routes.
		if code, body := f.do(t, "GET", "/api/v1/x/fake/hello/ann"); code != 200 || body != "hello ann" {
			t.Errorf("module route: %d %q", code, body)
		}
		for _, p := range []string{"/api/v1/x/fake/nope", "/api/v1/x/other/hello/ann", "/api/v1/x/fake"} {
			if code, body := f.do(t, "GET", p); code != 404 || body != "core 404" {
				t.Errorf("GET %s: %d %q, want the core's 404", p, code, body)
			}
		}
		// Migrations, in the module's namespace.
		if v, err := db.SchemaVersion(ctx, "fake"); err != nil || v != 1 || !tableExists(t, db, "fake_items") {
			t.Errorf("module migration: version %d, err %v", v, err)
		}
		// An audited write in the module's transaction.
		if code, body := f.do(t, "POST", "/api/v1/x/fake/items/first"); code != 201 {
			t.Errorf("audited write: %d %q", code, body)
		}
		if len(f.audit.entries) != 1 || f.audit.entries[0].Action != "fake.create" || f.audit.entries[0].Reason != "user-1" {
			t.Errorf("audit entries: %+v", f.audit.entries)
		}
		// Jobs: added under the module's prefix and run.
		waitFor(t, "the module's job to run", func() bool { return m.ticks.Load() >= 1 })
		if !f.sched.Trigger("fake.tick") {
			t.Error("the module's job is not scheduled as fake.tick")
		}
		// Events, as JSON.
		f.publish(t, "x")
		got := m.received()
		if len(got) != 1 || got[0].Kind != "thing.done" || string(got[0].Payload) != `{"Name":"x"}` {
			t.Errorf("module events: %+v", got)
		}
		// Its own settings, namespaced.
		if v := st.get("modules.fake.started"); v != "true" {
			t.Errorf("module setting stored as %q under modules.fake.started", v)
		}

		// Off: routes, jobs and subscriptions are taken back; data stays.
		if err := f.host.Disable(ctx, "fake"); err != nil {
			t.Fatal(err)
		}
		if v := st.get(modhost.EnabledKey); v != `[]` {
			t.Errorf("setting after Disable: %s", v)
		}
		if code, _ := f.do(t, "GET", "/api/v1/x/fake/hello/ann"); code != 404 {
			t.Errorf("a module that is off still answers: %d", code)
		}
		if f.sched.Trigger("fake.tick") {
			t.Error("the job of a module that is off is still scheduled")
		}
		f.publish(t, "y")
		if n := len(m.received()); n != 1 {
			t.Errorf("a module that is off still gets events: %d", n)
		}
		if m.stopped.Load() != 1 || !tableExists(t, db, "fake_items") {
			t.Errorf("Stop called %d times; table kept: %v", m.stopped.Load(), tableExists(t, db, "fake_items"))
		}

		// On again: everything comes back.
		if err := f.host.Enable(ctx, "fake"); err != nil {
			t.Fatal(err)
		}
		if v := st.get(modhost.EnabledKey); v != `["fake"]` {
			t.Errorf("setting after Enable: %s", v)
		}
		if code, _ := f.do(t, "GET", "/api/v1/x/fake/hello/bo"); code != 200 {
			t.Errorf("the route is not back after Enable: %d", code)
		}
	})
}

// TestFailedStartTakesEverythingBack: a module whose Start fails is off, and nothing it
// registered stays.
func TestFailedStartTakesEverythingBack(t *testing.T) {
	ctx := context.Background()
	st := newSettings()
	m := &fakeModule{id: "broken", failWith: errors.New("no license")}
	f := newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)), st, nil, m)
	err := f.host.Enable(ctx, "broken")
	if err == nil || !strings.Contains(err.Error(), "no license") {
		t.Fatalf("Enable of a failing module: %v", err)
	}
	if len(f.host.Enabled()) != 0 || st.get(modhost.EnabledKey) != "" {
		t.Errorf("a module that failed to start is on: %v, setting %q", f.host.Enabled(), st.get(modhost.EnabledKey))
	}
	if code, _ := f.do(t, "GET", "/api/v1/x/broken/hello/ann"); code != 404 {
		t.Errorf("route of a failed module: %d", code)
	}
	if f.sched.Trigger("broken.tick") {
		t.Error("job of a failed module is scheduled")
	}
	f.publish(t, "x")
	if len(m.received()) != 0 {
		t.Error("a failed module gets events")
	}
}

// TestLockedUnknownAndUnreadable: the deployment's lock wins over the setting; an unknown module
// cannot be turned on; a setting that cannot be read starts nothing.
func TestLockedUnknownAndUnreadable(t *testing.T) {
	ctx := context.Background()
	st := newSettings(modhost.EnabledKey, `["fake","gone"]`)
	m := &fakeModule{id: "fake"}
	f := newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)), st, []string{"fake"}, m)
	if err := f.host.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.host.Enabled()) != 0 {
		t.Errorf("a locked module started: %v", f.host.Enabled())
	}
	if err := f.host.Enable(ctx, "fake"); !errors.Is(err, modhost.ErrLocked) {
		t.Errorf("Enable of a locked module: %v", err)
	}
	if err := f.host.Enable(ctx, "gone"); !errors.Is(err, modhost.ErrUnknown) {
		t.Errorf("Enable of a module not in the build: %v", err)
	}
	if got := f.host.Modules(); len(got) != 1 || !got[0].Locked || got[0].Enabled || got[0].Info.Name != "module.fake.name" {
		t.Errorf("Modules() = %+v", got)
	}

	unreadable := newSettings(modhost.EnabledKey, `["fake"]`)
	unreadable.failGet = true
	g := newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)), unreadable, nil, &fakeModule{id: "fake"})
	if err := g.host.Start(ctx); err == nil || len(g.host.Enabled()) != 0 {
		t.Errorf("an unreadable setting: err %v, started %v; want an error and nothing started", err, g.host.Enabled())
	}
}

// TestAuditAndPermissionsFailClosed: a module cannot write the core's audit actions, and
// without the audit log or the authorizer its writes and checks fail.
func TestAuditAndPermissionsFailClosed(t *testing.T) {
	ctx := context.Background()
	db := dbxtest.Open(t, dbxtest.SQLiteURL(t))
	if _, err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	var host module.Host
	probe := &probeModule{got: &host}
	bus, sched := events.New(nil), jobs.New(db, jobs.Options{})
	h, err := modhost.New(modhost.Deps{DB: db, Events: bus, Jobs: sched, Settings: newSettings(), Modules: []module.Module{probe}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Stop(ctx)
	if err := h.Enable(ctx, "probe"); err != nil {
		t.Fatal(err)
	}
	err = host.DB().InTx(ctx, func(tx module.Tx) error {
		return host.Audit().Append(ctx, tx, module.Actor{}, module.AuditEntry{Action: "resource.publish"})
	})
	if err == nil || !strings.Contains(err.Error(), "probe.") {
		t.Errorf("a module wrote a core audit action: %v", err)
	}
	err = host.DB().InTx(ctx, func(tx module.Tx) error {
		return host.Audit().Append(ctx, tx, module.Actor{}, module.AuditEntry{Action: "probe.export"})
	})
	if err == nil {
		t.Error("an audit entry was accepted without an audit log")
	}
	if ok, err := host.Authz().Allowed(ctx, module.Actor{UserID: "u"}, "probe.export", module.Target{}); ok || err == nil {
		t.Errorf("without an authorizer: allowed=%v err=%v, want a refusal", ok, err)
	}
	if _, ok := host.Authz().Actor(ctx); ok {
		t.Error("without an authorizer there is an actor")
	}
	if host.DB().Dialect() != "sqlite" {
		t.Errorf("Dialect() = %q", host.DB().Dialect())
	}
}

type probeModule struct{ got *module.Host }

func (*probeModule) ID() string            { return "probe" }
func (*probeModule) Describe() module.Info { return module.Info{} }
func (p *probeModule) Start(_ context.Context, h module.Host) error {
	*p.got = h
	return nil
}
