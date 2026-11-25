// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package modhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"slices"
	"strings"
	"sync"

	"github.com/code2rich/rigdepot/ext/module"
	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/events"
	"github.com/code2rich/rigdepot/internal/server/jobs"
)

// RoutePrefix is where module routes are mounted: RoutePrefix + "<id>/…".
const RoutePrefix = "/api/v1/x/"

// EnabledKey is the setting that lists the modules that are on, as a JSON array of IDs.
const EnabledKey = "modules.enabled"

// Errors of Enable and Disable.
var (
	ErrUnknown = errors.New("modhost: this module is not in this build")
	ErrLocked  = errors.New("modhost: the deployment disabled this module")
	// ErrStart wraps the error of a module's Start (or its panic): the module stays off.
	ErrStart = errors.New("modhost: the module did not start")
)

// SettingsStore is the part of the settings domain the host needs: JSON values by key. A read
// that fails returns an error, never "not set".
type SettingsStore interface {
	Get(ctx context.Context, key string) (json.RawMessage, bool, error)
	Set(ctx context.Context, key string, value json.RawMessage) error
}

// AuditAppender is the part of the audit domain the host needs: append an entry in the
// transaction of the change.
type AuditAppender interface {
	Append(ctx context.Context, tx *dbx.Tx, actor module.Actor, e module.AuditEntry) error
}

// Deps are what the host hands out to modules. DB, Events and Jobs are required. The others may
// be nil until their domain packages exist; what depends on them then fails closed: no audit
// means a module's audited writes fail, no authorizer means everything is denied, and no
// settings store means no module is on.
type Deps struct {
	DB       *dbx.DB
	Events   *events.Bus
	Jobs     *jobs.Scheduler
	Audit    AuditAppender
	Authz    module.Authorizer
	Settings SettingsStore
	Log      *slog.Logger
	// Locked are the modules the deployment disabled (RGD_MODULES_DISABLED); they never start.
	Locked []string
	// Modules are the modules compiled in; nil means module.All().
	Modules []module.Module
}

// Host runs the modules.
type Host struct {
	d    Deps
	log  *slog.Logger
	mods map[string]module.Module
	ids  []string // sorted

	opMu    sync.Mutex // one Start, Enable, Disable or Stop at a time
	mu      sync.RWMutex
	running map[string]*instance
}

// New returns a host for the modules in d.
func New(d Deps) (*Host, error) {
	if d.DB == nil || d.Events == nil || d.Jobs == nil {
		return nil, errors.New("modhost: DB, Events and Jobs are required")
	}
	if d.Log == nil {
		d.Log = logging.Discard()
	}
	if d.Modules == nil {
		d.Modules = module.All()
	}
	h := &Host{d: d, log: d.Log, mods: map[string]module.Module{}, running: map[string]*instance{}}
	for _, m := range d.Modules {
		id := m.ID()
		if _, dup := h.mods[id]; dup || slices.Contains(module.Reserved, id) {
			return nil, fmt.Errorf("modhost: module %q is reserved or given twice", id)
		}
		h.mods[id] = m
		h.ids = append(h.ids, id)
	}
	slices.Sort(h.ids)
	return h, nil
}

func (h *Host) locked(id string) bool { return slices.Contains(h.d.Locked, id) }

// Status is one module as the settings page shows it.
type Status struct {
	ID      string
	Info    module.Info
	Enabled bool // running now
	Locked  bool // disabled by the deployment
}

// Modules returns every module compiled in, by ID.
func (h *Host) Modules() []Status {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Status, 0, len(h.ids))
	for _, id := range h.ids {
		_, on := h.running[id]
		out = append(out, Status{ID: id, Info: h.mods[id].Describe(), Enabled: on, Locked: h.locked(id)})
	}
	return out
}

// Enabled returns the IDs of the modules running now, sorted.
func (h *Host) Enabled() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return slices.Sorted(func(yield func(string) bool) {
		for id := range h.running {
			if !yield(id) {
				return
			}
		}
	})
}

// Start starts the modules the setting turns on. A module that fails to start stays off and the
// others still start; the errors are returned together. When the setting cannot be read, no
// module starts.
func (h *Host) Start(ctx context.Context) error {
	h.opMu.Lock()
	defer h.opMu.Unlock()
	ids, err := h.enabledSetting(ctx)
	if err != nil {
		return fmt.Errorf("modhost: cannot read which modules are on, so none was started: %w", err)
	}
	var errs []error
	for _, id := range ids {
		m, ok := h.mods[id]
		switch {
		case !ok:
			h.log.Info("a module that is on in the settings is not in this build", "module", id)
			continue
		case h.locked(id):
			h.log.Info("a module that is on in the settings is disabled by the deployment", "module", id)
			continue
		}
		if err := h.start(ctx, m); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Enable turns a module on: it starts it, then records it in the setting.
func (h *Host) Enable(ctx context.Context, id string) error {
	h.opMu.Lock()
	defer h.opMu.Unlock()
	m, ok := h.mods[id]
	switch {
	case !ok:
		return ErrUnknown
	case h.locked(id):
		return ErrLocked
	case h.d.Settings == nil:
		return errors.New("modhost: settings are not available")
	}
	ids, err := h.enabledSetting(ctx)
	if err != nil {
		return err
	}
	h.mu.RLock()
	_, on := h.running[id]
	h.mu.RUnlock()
	if !on {
		if err := h.start(ctx, m); err != nil {
			return err
		}
	}
	if slices.Contains(ids, id) {
		return nil
	}
	if err := h.writeEnabled(ctx, append(ids, id)); err != nil {
		if !on {
			h.stop(ctx, id)
		}
		return err
	}
	return nil
}

// Disable turns a module off: it records that in the setting, then stops the module. Its tables
// and data stay.
func (h *Host) Disable(ctx context.Context, id string) error {
	h.opMu.Lock()
	defer h.opMu.Unlock()
	if h.d.Settings == nil {
		return errors.New("modhost: settings are not available")
	}
	ids, err := h.enabledSetting(ctx)
	if err != nil {
		return err
	}
	if _, ok := h.mods[id]; !ok && !slices.Contains(ids, id) {
		return ErrUnknown
	}
	if slices.Contains(ids, id) {
		if err := h.writeEnabled(ctx, slices.DeleteFunc(ids, func(s string) bool { return s == id })); err != nil {
			return err
		}
	}
	h.stop(ctx, id)
	return nil
}

// Stop stops every running module, without changing the setting: they start again with the
// server.
func (h *Host) Stop(ctx context.Context) {
	h.opMu.Lock()
	defer h.opMu.Unlock()
	for _, id := range h.Enabled() {
		h.stop(ctx, id)
	}
}

func (h *Host) enabledSetting(ctx context.Context) ([]string, error) {
	if h.d.Settings == nil {
		return nil, nil
	}
	raw, ok, err := h.d.Settings.Get(ctx, EnabledKey)
	if err != nil || !ok {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, fmt.Errorf("modhost: the setting %s is not a list of module IDs: %w", EnabledKey, err)
	}
	return ids, nil
}

func (h *Host) writeEnabled(ctx context.Context, ids []string) error {
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	if ids == nil {
		ids = []string{} // [] rather than null
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return h.d.Settings.Set(ctx, EnabledKey, raw)
}

// instance is a running module and what it registered.
type instance struct {
	id     string
	mod    module.Module
	mux    *http.ServeMux
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	stopped bool
	jobs    []string
	unsubs  []func()
}

func (h *Host) start(ctx context.Context, m module.Module) error {
	id := m.ID()
	inst := &instance{id: id, mod: m, mux: http.NewServeMux()}
	inst.ctx, inst.cancel = context.WithCancel(context.WithoutCancel(ctx))
	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v\n%s", p, debug.Stack())
			}
		}()
		return m.Start(inst.ctx, &view{h: h, inst: inst})
	}()
	if err != nil {
		h.teardown(ctx, inst)
		h.log.Error("module did not start", "module", id, "err", err)
		return fmt.Errorf("%w: %s: %w", ErrStart, id, err)
	}
	h.mu.Lock()
	h.running[id] = inst
	h.mu.Unlock()
	h.log.Info("module started", "module", id)
	return nil
}

func (h *Host) stop(ctx context.Context, id string) {
	h.mu.Lock()
	inst, ok := h.running[id]
	delete(h.running, id)
	h.mu.Unlock()
	if ok {
		h.teardown(ctx, inst)
		h.log.Info("module stopped", "module", id)
	}
}

// teardown takes back what the module registered.
func (h *Host) teardown(ctx context.Context, inst *instance) {
	inst.mu.Lock()
	inst.stopped = true
	jobNames, unsubs := inst.jobs, inst.unsubs
	inst.jobs, inst.unsubs = nil, nil
	inst.mu.Unlock()
	inst.cancel()
	for _, unsub := range unsubs {
		unsub()
	}
	for _, name := range jobNames {
		if err := h.d.Jobs.Remove(ctx, name); err != nil {
			h.log.Error("module job did not stop", "module", inst.id, "job", name, "err", err)
		}
	}
	if s, ok := inst.mod.(module.Stopper); ok {
		if err := s.Stop(ctx); err != nil {
			h.log.Error("module stop failed", "module", inst.id, "err", err)
		}
	}
}

// Handler serves the routes of the running modules: requests under RoutePrefix + "<id>/". Paths
// of modules that are off or not in the build, and paths a module did not register, go to
// notFound.
func (h *Host) Handler(notFound http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, RoutePrefix)
		id, _, _ := strings.Cut(rest, "/")
		h.mu.RLock()
		inst := h.running[id]
		h.mu.RUnlock()
		if !ok || inst == nil {
			notFound.ServeHTTP(w, r)
			return
		}
		prefix := RoutePrefix + id
		r2 := new(http.Request)
		*r2 = *r
		r2.URL = new(url.URL)
		*r2.URL = *r.URL
		r2.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
		r2.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, prefix)
		if r2.URL.Path == "" {
			r2.URL.Path = "/"
		}
		_, pattern := inst.mux.Handler(r2)
		if pattern == "" {
			notFound.ServeHTTP(w, r)
			return
		}
		logging.SetRoute(r.Context(), prefix+patternPath(pattern))
		inst.mux.ServeHTTP(w, r2)
	})
}

// patternPath returns the path of a ServeMux pattern: "GET /items/{id}" gives "/items/{id}".
func patternPath(pattern string) string {
	if _, p, ok := strings.Cut(pattern, " "); ok {
		pattern = strings.TrimSpace(p)
	}
	if i := strings.Index(pattern, "/"); i > 0 {
		pattern = pattern[i:]
	}
	return pattern
}
