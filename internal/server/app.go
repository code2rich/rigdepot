// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/config"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/httpapi/authnapi"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// Mounts are the handlers the server root does not build itself.
type Mounts struct {
	// API serves everything under /api/. It receives the full path
	// (/api/v1/…), with the base URL's path already removed. Nil means no
	// API yet: /api/ answers 404 with a problem document.
	API http.Handler
	// Web serves the console: every path that is not /api/, /healthz or
	// /readyz. Nil means 404.
	Web http.Handler
}

// App is the assembled server: configuration, logger, database and routes.
type App struct {
	cfg    *config.Config
	log    *slog.Logger
	db     *dbx.DB
	mounts Mounts
	// bg runs events, jobs, the mail outbox and modules; nil outside serve.
	bg *Background

	// The domain services (openServices) and what other packages provide
	// to the API (hooks).
	keys     *masterkey.Keyring
	audit    *audit.Log
	access   *access.Service
	authn    *authn.Service
	authnOps *authnapi.Ops // the sign-in operations and middleware
	settings *settings.Service
	hooks    hooks

	checks   []ReadyCheck
	draining atomic.Bool
}

// ReadyCheck is one item of /readyz. A failing critical check makes the
// server not ready (503); a failing non-critical one makes it degraded,
// which is still ready (200): the mail server not being configured or a
// repository fetch failing should not take the server out of a load
// balancer.
type ReadyCheck struct {
	Name     string
	Critical bool
	Check    func(ctx context.Context) error
}

// NewApp assembles the server around an open, migrated database.
func NewApp(cfg *config.Config, log *slog.Logger, db *dbx.DB, mounts Mounts) *App {
	a := &App{cfg: cfg, log: log, db: db, mounts: mounts}
	a.AddReadyCheck(ReadyCheck{Name: "database", Critical: true, Check: db.Ping})
	a.AddReadyCheck(ReadyCheck{Name: "migrations", Critical: true, Check: func(ctx context.Context) error {
		pending, err := migrations.Pending(ctx, db)
		if err != nil {
			return err
		}
		if len(pending) > 0 {
			return fmt.Errorf("%d migrations have not run", len(pending))
		}
		return nil
	}})
	// "master_key" joins in openServices, which loads it.
	return a
}

// Background returns the events, jobs, outbox and modules of a serving app, for apiMounts to
// hand to the domain services; nil when nothing runs in the background.
func (a *App) Background() *Background { return a.bg }

// AddReadyCheck adds an item to /readyz. Domain services add theirs while
// the server is assembled, before it serves.
func (a *App) AddReadyCheck(c ReadyCheck) { a.checks = append(a.checks, c) }

// Handler returns the root handler: /healthz and /readyz, the API and the
// console, all under the path of the base URL, with request IDs, request
// logging and panic recovery around them. The probes also answer at the
// root, so that a container health check does not need to know the path.
func (a *App) Handler() http.Handler {
	inner := http.NewServeMux()
	inner.HandleFunc("GET /healthz", a.healthz)
	inner.HandleFunc("GET /readyz", a.readyz)
	api := a.mounts.API
	if api == nil {
		api = http.HandlerFunc(notFound)
	}
	inner.Handle("/api/", api)
	web := a.mounts.Web
	if web == nil {
		web = http.HandlerFunc(notFound)
	}
	inner.Handle("/", web)

	var h http.Handler = inner
	if prefix := a.cfg.BaseURL.Path; prefix != "" {
		outer := http.NewServeMux()
		outer.HandleFunc("GET /healthz", a.healthz)
		outer.HandleFunc("GET /readyz", a.readyz)
		outer.Handle(prefix+"/", http.StripPrefix(prefix, inner))
		outer.Handle(prefix, http.RedirectHandler(prefix+"/", http.StatusMovedPermanently))
		outer.HandleFunc("/", notFound)
		h = outer
	}
	return a.observe(h)
}

// shutdownTimeout is how long requests in flight get to finish on SIGTERM.
const shutdownTimeout = 15 * time.Second

// Serve serves on ln until ctx is done, then shuts down gracefully: /readyz
// turns 503 at once so that a load balancer stops sending requests, and
// requests in flight get shutdownTimeout to finish.
func (a *App) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(a.log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	var redirect *http.Server
	if a.cfg.ServesTLS() {
		tlsCfg, err := a.tlsConfig()
		if err != nil {
			return err
		}
		ln = tls.NewListener(ln, tlsCfg)
		if addr := a.cfg.TLS.HTTPRedirectListen; addr != "" {
			rln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("listen %s (HTTP redirect): %w", addr, err)
			}
			redirect = &http.Server{Handler: a.redirectToHTTPS(), ReadHeaderTimeout: 10 * time.Second}
			go func() {
				if err := redirect.Serve(rln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					a.log.Error("HTTP redirect listener stopped", "err", err)
				}
			}()
		}
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	a.log.Info("serving", "listen", ln.Addr().String(), "base_url", a.cfg.BaseURL.String(), "tls", a.cfg.ServesTLS())

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	a.draining.Store(true)
	a.log.Info("shutting down", "timeout", shutdownTimeout.String())
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := srv.Shutdown(sctx)
	if redirect != nil {
		err = errors.Join(err, redirect.Shutdown(sctx))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		a.log.Warn("requests still running at the end of the shutdown timeout were cut off")
		srv.Close()
	}
	if serveErr := <-errc; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		err = errors.Join(err, serveErr)
	}
	return err
}

// tlsConfig loads the certificate given in the configuration. Reloading it
// on SIGHUP and ACME are task M1-40.
func (a *App) tlsConfig() (*tls.Config, error) {
	if len(a.cfg.TLS.ACMEDomains) > 0 {
		return nil, errors.New("automatic certificates (tls.acme_domains) are not implemented in this version yet; use tls.cert_file and tls.key_file or a reverse proxy")
	}
	cert, err := tls.LoadX509KeyPair(a.cfg.TLS.CertFile, a.cfg.TLS.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load the TLS certificate: %w", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2", "http/1.1"},
	}, nil
}

// redirectToHTTPS sends every plain HTTP request to the same path on the
// base URL.
func (a *App) redirectToHTTPS() http.Handler {
	base := *a.cfg.BaseURL
	base.Scheme = "https"
	origin := base.Scheme + "://" + base.Host
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := origin + r.URL.RequestURI()
		if !strings.HasPrefix(r.URL.Path, base.Path) {
			target = origin + base.Path + "/"
		}
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	})
}
