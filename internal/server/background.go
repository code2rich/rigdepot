// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"errors"
	"time"

	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/server/events"
	"github.com/code2rich/rigdepot/internal/server/jobs"
	"github.com/code2rich/rigdepot/internal/server/modhost"
	"github.com/code2rich/rigdepot/internal/server/notify"
	"github.com/code2rich/rigdepot/locales"
)

// Background is what runs beside the requests: domain events, background jobs, the mail outbox
// and the optional modules. Domain services publish on Events; their jobs go on Jobs.
type Background struct {
	Events      *events.Bus
	Jobs        *jobs.Scheduler
	JobCounters *jobs.Counters // job outcomes, for the metrics endpoint (M1-41)
	Notify      *notify.Service
	Modules     *modhost.Host
}

// backgroundShutdown is how long jobs, event handlers and modules get to stop.
const backgroundShutdown = 15 * time.Second

// startBackground assembles and starts the background parts. The returned function stops them.
//
// Still to connect once their domain packages exist: the settings store (M1-10) for the mail
// settings (notify.Options.Config, then the readiness item "mail" from Notify.Check) and for
// modules.enabled (modhost.Deps.Settings); the audit log (M1-10) and permissions (M1-12) for
// modules (modhost.Deps.Audit, Authz); the module routes (Modules.Handler) under /api/v1/x/ in
// apiMounts. Until then mail waits in the outbox and no module is on.
func startBackground(ctx context.Context, app *App) (*Background, func(), error) {
	b := &Background{Events: events.New(app.log), JobCounters: jobs.NewCounters()}
	b.Jobs = jobs.New(app.db, jobs.Options{Log: app.log, Metrics: b.JobCounters})
	cat, err := i18n.Load(locales.FS, "server")
	if err != nil {
		return nil, nil, err
	}
	b.Notify, err = notify.New(notify.Options{DB: app.db, Jobs: b.Jobs, Catalog: cat, DefaultLocale: app.cfg.DefaultLocale, Log: app.log})
	if err != nil {
		return nil, nil, err
	}
	b.Modules, err = modhost.New(modhost.Deps{DB: app.db, Events: b.Events, Jobs: b.Jobs, Log: app.log, Locked: app.cfg.ModulesDisabled})
	if err != nil {
		return nil, nil, err
	}
	b.Jobs.Start()
	if err := b.Modules.Start(ctx); err != nil {
		app.log.Error("modules", "err", err)
	}
	stop := func() {
		sctx, cancel := context.WithTimeout(context.Background(), backgroundShutdown)
		defer cancel()
		b.Modules.Stop(sctx)
		err := errors.Join(b.Jobs.Stop(sctx), b.Events.Close(sctx))
		if err != nil {
			app.log.Warn("background work still running at shutdown", "err", err)
		}
	}
	return b, stop, nil
}
