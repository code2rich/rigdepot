// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/code2rich/rigdepot/internal/logging"
)

// RequestIDHeader carries the request ID back to the client, so that a
// person reporting a problem can quote it and an administrator can find the
// log line.
const RequestIDHeader = "X-Request-Id"

func (a *App) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyStatus is the body of /readyz. It names the checks and their state
// but not the errors: the endpoint is public, and errors can carry
// internals. The errors go to the log.
type readyStatus struct {
	Status string            `json:"status"` // ok | degraded | unavailable
	Checks map[string]string `json:"checks"` // name -> ok | degraded | failed
}

// readyTimeout bounds each check, so that a hanging database does not hang
// the probe.
const readyTimeout = 3 * time.Second

func (a *App) readyz(w http.ResponseWriter, r *http.Request) {
	out := readyStatus{Status: "ok", Checks: map[string]string{}}
	if a.draining.Load() {
		out.Status = "unavailable"
		out.Checks["shutdown"] = "failed"
		writeJSON(w, http.StatusServiceUnavailable, out)
		return
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, c := range a.checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
			defer cancel()
			err := c.Check(ctx)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				out.Checks[c.Name] = "ok"
			case c.Critical:
				out.Checks[c.Name] = "failed"
				out.Status = "unavailable"
				a.log.WarnContext(r.Context(), "readiness check failed", "check", c.Name, "err", err)
			default:
				out.Checks[c.Name] = "degraded"
				if out.Status == "ok" {
					out.Status = "degraded"
				}
				a.log.InfoContext(r.Context(), "readiness check degraded", "check", c.Name, "err", err)
			}
		}()
	}
	wg.Wait()
	code := http.StatusOK
	if out.Status == "unavailable" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, out)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// problem is an RFC 9457 problem document with the fields
// docs/architecture/06-server.md#api-风格与契约 adds.
type problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	RequestID string `json:"request_id,omitempty"`
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, code string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(problem{
		Type: "about:blank", Title: http.StatusText(status), Status: status, Code: code,
		RequestID: logging.RequestID(r.Context()),
	})
}

func notFound(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusNotFound, "not_found")
}

// observe wraps every request: it gives the request an ID, recovers from a
// panic with a 500, and writes one log line at the end with the request ID,
// method, route template (not the real path, which can contain names), status,
// duration and user. Request bodies and query values are never logged.
func (a *App) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		req := &logging.Request{ID: newRequestID()}
		ctx := logging.WithRequest(r.Context(), req)
		r = r.WithContext(ctx)
		w.Header().Set(RequestIDHeader, req.ID)
		rec := &statusRecorder{ResponseWriter: w}

		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				a.log.ErrorContext(ctx, "handler panicked", "panic", p)
				if !rec.wrote {
					writeProblem(rec, r, http.StatusInternalServerError, "internal")
				}
			}
			route := req.Route
			if route == "" {
				route = r.Pattern
			}
			if route == "" {
				route = "unmatched"
			}
			level := slog.LevelInfo
			if rec.status() >= 500 {
				level = slog.LevelError
			}
			attrs := []any{"method", r.Method, "route", route, "status", rec.status(), "duration_ms", time.Since(start).Milliseconds()}
			if req.UserID != "" {
				attrs = append(attrs, "user_id", req.UserID)
			}
			a.log.Log(ctx, level, "request", attrs...)
		}()
		next.ServeHTTP(rec, r)
	})
}

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(errors.New("no randomness: " + err.Error()))
	}
	return hex.EncodeToString(b[:])
}

type statusRecorder struct {
	http.ResponseWriter
	code  int
	wrote bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.code, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.code, s.wrote = http.StatusOK, true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) status() int {
	if !s.wrote {
		return http.StatusOK
	}
	return s.code
}

// Unwrap lets http.ResponseController reach the real writer (flushing,
// deadlines).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
