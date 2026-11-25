// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// marker stands for a secret. A test fails if it shows up in a log line.
const marker = "Zq8marker4Kd9secretValue0x7"

func TestSensitiveKey(t *testing.T) {
	for _, name := range []string{
		"password", "smtp_password", "SMTPPassword", "client-secret", "ClientSecret", "token",
		"device_token", "Authorization", "cookie", "api_key", "apiKey", "master_key", "MasterKey",
		"private_key", "database_url", "DatabaseURL", "dsn", "setup_code", "credentials",
	} {
		if !SensitiveKey(name) {
			t.Errorf("SensitiveKey(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"user_id", "route", "status", "key", "resource_key", "tokens_total", "passthrough",
		"auth_method", "duration_ms", "listen", "base_url",
	} {
		if SensitiveKey(name) {
			t.Errorf("SensitiveKey(%q) = true, want false", name)
		}
	}
}

func TestRedact(t *testing.T) {
	tests := []struct{ in, want string }{
		{"device token " + brand.CLI + "d_" + marker + " rejected", "device token " + brand.CLI + "d_" + Redacted + " rejected"},
		{"access token " + brand.CLI + "_" + marker, "access token " + brand.CLI + "_" + Redacted},
		{"Authorization: Bearer " + marker, "Authorization: Bearer " + Redacted},
		{"dial postgres://app:" + marker + "@db.example.com:5432/x failed", "dial postgres://app:" + Redacted + "@db.example.com:5432/x failed"},
		{"host=db.example.com password=" + marker + " sslmode=require", "host=db.example.com password=" + Redacted + " sslmode=require"},
		{`{"client_secret":"` + marker + `"}`, `{"client_secret":"` + Redacted + `"}`},
		{"smtp: auth failed for user x, pwd: " + marker, "smtp: auth failed for user x, pwd: " + Redacted},
		// Things that look a little like secrets but are not.
		{"build tag " + brand.CLI + "_core_only", "build tag " + brand.CLI + "_core_only"},
		{"listening on https://hub.example.com/", "listening on https://hub.example.com/"},
		{"5 tokens issued", "5 tokens issued"},
	}
	for _, tt := range tests {
		if got := Redact(tt.in); got != tt.want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", tt.in, got, tt.want)
		}
	}
}

// logLine runs fn against a JSON logger and returns the one line it wrote.
func logLine(t *testing.T, fn func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	fn(New(&buf, slog.LevelDebug, JSON))
	line := strings.TrimSpace(buf.String())
	if !json.Valid([]byte(line)) {
		t.Fatalf("not one JSON line: %q", buf.String())
	}
	return line
}

func TestLoggerNeverWritesTheSecret(t *testing.T) {
	u, _ := url.Parse("postgres://app:" + marker + "@db.example.com/x")
	cases := map[string]func(*slog.Logger){
		"sensitive name":          func(l *slog.Logger) { l.Info("login", "password", marker) },
		"sensitive name, int":     func(l *slog.Logger) { l.Info("login", "pin_secret", 42, "note", marker[:3]) },
		"token in message":        func(l *slog.Logger) { l.Info("token " + brand.CLI + "_" + marker + " used") },
		"token in a string value": func(l *slog.Logger) { l.Info("x", "detail", "got "+brand.CLI+"d_"+marker) },
		"error value":             func(l *slog.Logger) { l.Error("x", "err", errors.New("password="+marker)) },
		"stringer value":          func(l *slog.Logger) { l.Info("x", "db", u) },
		"inside a group":          func(l *slog.Logger) { l.Info("x", slog.Group("smtp", "host", "mail.example.com", "password", marker)) },
		"With attributes":         func(l *slog.Logger) { l.With("token", marker).Info("x") },
		"With a group":            func(l *slog.Logger) { l.WithGroup("g").Info("x", "secret", marker) },
		"map value":               func(l *slog.Logger) { l.Info("x", "settings", map[string]string{"k": "password=" + marker}) },
		"bytes value":             func(l *slog.Logger) { l.Info("x", "body", []byte("Bearer "+marker)) },
		"log valuer":              func(l *slog.Logger) { l.Info("x", "v", valuer{}) },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			line := logLine(t, fn)
			if strings.Contains(line, marker) {
				t.Errorf("the secret reached the log: %s", line)
			}
		})
	}
}

// TestDisclosedIsWritten: the setup code is logged on purpose under a name that is otherwise
// redacted; the same name with a plain value is still redacted.
func TestDisclosedIsWritten(t *testing.T) {
	line := logLine(t, func(l *slog.Logger) { l.Warn("setup", "setup_code", Disclosed(marker)) })
	if !strings.Contains(line, `"setup_code":"`+marker+`"`) {
		t.Errorf("the disclosed value is not in the log: %s", line)
	}
	line = logLine(t, func(l *slog.Logger) { l.Warn("setup", "setup_code", marker) })
	if strings.Contains(line, marker) {
		t.Errorf("a plain setup_code reached the log: %s", line)
	}
}

type valuer struct{}

func (valuer) LogValue() slog.Value { return slog.StringValue("token=" + marker) }

func TestOrdinaryAttributesAreKept(t *testing.T) {
	line := logLine(t, func(l *slog.Logger) {
		l.Info("request", "route", "/api/v1/resources/{code}", "status", 200, "user_id", "u-1")
	})
	for _, want := range []string{`"route":"/api/v1/resources/{code}"`, `"status":200`, `"user_id":"u-1"`, `"msg":"request"`} {
		if !strings.Contains(line, want) {
			t.Errorf("%s missing from %s", want, line)
		}
	}
}

func TestRequestIDFromContext(t *testing.T) {
	line := logLine(t, func(l *slog.Logger) {
		l.InfoContext(WithRequestID(context.Background(), "req-123"), "x")
	})
	if !strings.Contains(line, `"request_id":"req-123"`) {
		t.Errorf("request_id missing: %s", line)
	}
	if got := RequestID(context.Background()); got != "" {
		t.Errorf("RequestID of an empty context = %q", got)
	}
}

func TestRequestDetailsAreFilledInLater(t *testing.T) {
	req := &Request{ID: "r1"}
	ctx := WithRequest(context.Background(), req)
	SetRoute(ctx, "/api/v1/resources/{code}")
	SetUser(ctx, "u-1")
	if req.Route != "/api/v1/resources/{code}" || req.UserID != "u-1" || RequestID(ctx) != "r1" {
		t.Errorf("got %+v", req)
	}
	// Outside a request these do nothing (and do not panic).
	SetRoute(context.Background(), "x")
	SetUser(context.Background(), "x")
}

func TestParse(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "INFO": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError} {
		if got, err := ParseLevel(in); err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("ParseLevel(loud) succeeded")
	}
	if f, err := ParseFormat("Text"); err != nil || f != Text {
		t.Errorf("ParseFormat(Text) = %v, %v", f, err)
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Error("ParseFormat(xml) succeeded")
	}
}

func TestTextFormatAndLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelWarn, Text)
	l.Info("hidden")
	l.Warn("shown", "token", marker)
	out := buf.String()
	if strings.Contains(out, "hidden") || !strings.Contains(out, "msg=shown") || strings.Contains(out, marker) {
		t.Errorf("unexpected output: %q", out)
	}
}
