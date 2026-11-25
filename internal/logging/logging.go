// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Format is the encoding of log lines.
type Format string

const (
	// JSON writes one JSON object per line. It is the default: systemd and
	// Docker both keep it as it is, and log collectors parse it.
	JSON Format = "json"
	// Text writes key=value pairs, easier on a terminal.
	Text Format = "text"
)

// ParseLevel accepts debug, info, warn and error, in any case.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", s)
}

// ParseFormat accepts json and text.
func ParseFormat(s string) (Format, error) {
	switch f := Format(strings.ToLower(strings.TrimSpace(s))); f {
	case JSON, Text:
		return f, nil
	}
	return "", fmt.Errorf("unknown log format %q (want json or text)", s)
}

// New returns a logger that writes to w and redacts secrets: values of
// sensitive attribute names, strings that look like this product's tokens,
// bearer credentials and passwords in URLs (see Redact). The request ID of
// the context (WithRequestID) is added to every record logged with one.
func New(w io.Writer, level slog.Leveler, format Format) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if format == Text {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(NewRedactingHandler(h))
}

// Discard returns a logger that drops everything, for tests and for callers
// that were given no logger.
func Discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

type requestKey struct{}

// Request is what the HTTP layer knows about the request being served. The
// server puts one into the context of every request; handlers further down
// fill in what only they know (the route template, the user), and the
// request log line written at the end reports it.
type Request struct {
	ID     string // request ID, also sent back in a response header
	Route  string // route template, such as /api/v1/resources/{code}; never the real path
	UserID string // who made the request, when known
}

// WithRequest returns a context that carries r. Loggers made by New add
// r.ID to every record logged with that context as "request_id".
func WithRequest(ctx context.Context, r *Request) context.Context {
	return context.WithValue(ctx, requestKey{}, r)
}

// RequestFrom returns the request carried by ctx, or nil.
func RequestFrom(ctx context.Context) *Request {
	r, _ := ctx.Value(requestKey{}).(*Request)
	return r
}

// WithRequestID is WithRequest for a request known only by its ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return WithRequest(ctx, &Request{ID: id})
}

// RequestID returns the ID of the request carried by ctx, or "".
func RequestID(ctx context.Context) string {
	if r := RequestFrom(ctx); r != nil {
		return r.ID
	}
	return ""
}

// SetRoute records the route template of the request carried by ctx, for
// the request log. It does nothing outside a request.
func SetRoute(ctx context.Context, route string) {
	if r := RequestFrom(ctx); r != nil {
		r.Route = route
	}
}

// SetUser records who made the request carried by ctx, for the request log.
func SetUser(ctx context.Context, userID string) {
	if r := RequestFrom(ctx); r != nil {
		r.UserID = userID
	}
}
