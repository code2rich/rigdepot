// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package modhost

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/code2rich/rigdepot/ext/module"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/events"
	"github.com/code2rich/rigdepot/internal/server/jobs"
)

// view is one module's module.Host.
type view struct {
	h    *Host
	inst *instance
}

var _ module.Host = (*view)(nil)

func (v *view) Routes() module.Router { return router{v.inst} }

func (v *view) Migrate(ctx context.Context, fsys fs.FS) error {
	applied, err := v.h.d.DB.Migrate(ctx, v.inst.id, fsys)
	for _, m := range applied {
		v.h.log.Info("migration applied", "module", v.inst.id, "name", m.Name)
	}
	return err
}

func (v *view) DB() module.DB { return dbView{v.h.d.DB} }

func (v *view) Jobs() module.Scheduler { return scheduler{v} }

func (v *view) Events() module.Subscriber { return subscriber{v} }

func (v *view) Audit() module.AuditWriter { return auditor{v} }

func (v *view) Authz() module.Authorizer {
	if v.h.d.Authz == nil {
		return denyAll{}
	}
	return v.h.d.Authz
}

func (v *view) Settings() module.Settings { return settings{v} }

func (v *view) Logger() *slog.Logger { return v.h.log.With("module", v.inst.id) }

// track records something the module registered so that it is taken back when the module
// stops. It reports false, and registers nothing, once the module has stopped.
func (v *view) track(fn func(inst *instance)) bool {
	v.inst.mu.Lock()
	defer v.inst.mu.Unlock()
	if v.inst.stopped {
		return false
	}
	fn(v.inst)
	return true
}

type router struct{ inst *instance }

func (r router) Handle(pattern string, h http.Handler) { r.inst.mux.Handle(pattern, h) }

// dbView and txView give modules the database without the core's types.
type dbView struct{ db *dbx.DB }

func (d dbView) Dialect() string { return d.db.Dialect().String() }
func (d dbView) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return d.db.ExecContext(ctx, q, args...)
}
func (d dbView) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return d.db.QueryContext(ctx, q, args...)
}
func (d dbView) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return d.db.QueryRowContext(ctx, q, args...)
}
func (d dbView) InTx(ctx context.Context, fn func(module.Tx) error) error {
	return d.db.InTx(ctx, func(tx *dbx.Tx) error { return fn(txView{tx}) })
}

type txView struct{ tx *dbx.Tx }

func (t txView) Dialect() string { return t.tx.Dialect().String() }
func (t txView) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, q, args...)
}
func (t txView) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, q, args...)
}
func (t txView) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, q, args...)
}

// scheduler names the module's jobs <id>.<name>.
type scheduler struct{ v *view }

func (s scheduler) name(n string) string { return s.v.inst.id + "." + n }

func (s scheduler) Add(j module.Job) error {
	name := s.name(j.Name)
	var err error
	if !s.v.track(func(inst *instance) {
		err = s.v.h.d.Jobs.Add(jobs.Job{Name: name, Every: j.Every, Timeout: j.Timeout, Run: j.Run})
		if err == nil {
			inst.jobs = append(inst.jobs, name)
		}
	}) {
		return fmt.Errorf("modhost: module %s is off; job %s not added", s.v.inst.id, j.Name)
	}
	return err
}

func (s scheduler) Trigger(name string) bool { return s.v.h.d.Jobs.Trigger(s.name(name)) }

// subscriber hands events to modules as JSON.
type subscriber struct{ v *view }

func (s subscriber) Subscribe(names []string, fn func(context.Context, module.Event) error) {
	id := s.v.inst.id
	s.v.track(func(inst *instance) {
		unsub := s.v.h.d.Events.Subscribe("module "+id, names, func(ctx context.Context, ev events.Event) error {
			payload, err := json.Marshal(ev.Payload)
			if err != nil {
				return fmt.Errorf("encode the payload of %s: %w", ev.Name, err)
			}
			return fn(ctx, module.Event{ID: ev.ID, Name: ev.Name, At: ev.At, Payload: payload})
		})
		inst.unsubs = append(inst.unsubs, unsub)
	})
}

// auditor checks that a module writes only its own actions, in a transaction from its own DB.
type auditor struct{ v *view }

func (a auditor) Append(ctx context.Context, tx module.Tx, actor module.Actor, e module.AuditEntry) error {
	id := a.v.inst.id
	if !strings.HasPrefix(e.Action, id+".") {
		return fmt.Errorf("modhost: module %s may only write audit actions that start with %q, not %q", id, id+".", e.Action)
	}
	t, ok := tx.(txView)
	if !ok {
		return errors.New("modhost: audit entries are written in a transaction from Host.DB().InTx")
	}
	if a.v.h.d.Audit == nil {
		return errors.New("modhost: the audit log is not available, so the change cannot be made")
	}
	return a.v.h.d.Audit.Append(ctx, t.tx, actor, e)
}

// denyAll is the authorizer while the access domain is missing: nobody may do anything.
type denyAll struct{}

func (denyAll) Actor(context.Context) (module.Actor, bool) { return module.Actor{}, false }
func (denyAll) Allowed(context.Context, module.Actor, string, module.Target) (bool, error) {
	return false, errors.New("modhost: permissions are not available")
}

// settings keeps a module's settings under modules.<id>.<key>.
type settings struct{ v *view }

func (s settings) key(k string) (string, error) {
	if k == "" {
		return "", errors.New("modhost: empty settings key")
	}
	return "modules." + s.v.inst.id + "." + k, nil
}

func (s settings) Get(ctx context.Context, k string) (json.RawMessage, bool, error) {
	key, err := s.key(k)
	if err != nil {
		return nil, false, err
	}
	if s.v.h.d.Settings == nil {
		return nil, false, errors.New("modhost: settings are not available")
	}
	return s.v.h.d.Settings.Get(ctx, key)
}

func (s settings) Set(ctx context.Context, k string, value json.RawMessage) error {
	key, err := s.key(k)
	if err != nil {
		return err
	}
	if s.v.h.d.Settings == nil {
		return errors.New("modhost: settings are not available")
	}
	return s.v.h.d.Settings.Set(ctx, key, value)
}
