// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package logging

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/code2rich/rigdepot/internal/brand"
)

// Redacted replaces whatever was removed from a log record.
const Redacted = "[REDACTED]"

// sensitiveWords are parts of attribute names whose values are never logged.
// A name matches when one of its words (split at '_', '-', '.' and case
// changes) is in this list, so "smtp_password", "MasterKey" and
// "client-secret" match while "tokens_total" does not.
var sensitiveWords = map[string]bool{
	"password": true, "passwd": true, "pwd": true,
	"secret": true, "token": true, "authorization": true,
	"cookie": true, "credential": true, "credentials": true,
	"apikey": true, "dsn": true, "privatekey": true,
}

// sensitivePairs are two adjacent words that together name a secret, such as
// "api_key", "master_key" and "private_key". "key" on its own is too common
// ("resource_key", "message key") to redact.
var sensitivePairs = map[[2]string]bool{
	{"api", "key"}: true, {"master", "key"}: true, {"private", "key"}: true,
	{"secret", "key"}: true, {"access", "key"}: true, {"session", "id"}: true,
	{"database", "url"}: true, {"setup", "code"}: true,
}

// SensitiveKey reports whether values under this attribute or field name must
// not be logged.
func SensitiveKey(name string) bool {
	words := splitWords(name)
	for i, w := range words {
		if sensitiveWords[w] {
			return true
		}
		if i > 0 && sensitivePairs[[2]string{words[i-1], w}] {
			return true
		}
	}
	return false
}

func splitWords(name string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(name)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == '.' || r == ' ':
			flush()
		case isUpper(r) && i > 0 && (isLower(runes[i-1]) ||
			// The end of an acronym: the P of "SMTPPassword".
			isUpper(runes[i-1]) && i+1 < len(runes) && isLower(runes[i+1])):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }

var (
	// productToken matches the device tokens and access tokens this product
	// issues: a fixed prefix, then the random part and check digits. The
	// prefix stays readable so that a log still tells which kind it was.
	productToken = regexp.MustCompile(`\b(` + regexp.QuoteMeta(brand.CLI) + `d?_)[A-Za-z0-9_-]{16,}`)
	// bearer matches the credential of an Authorization header, whatever its
	// format.
	bearer = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
	// urlPassword matches the password in scheme://user:password@host.
	urlPassword = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^/\s:@]*:)[^@\s/]+@`)
	// keyValue matches password=..., token: ... and similar pairs inside free
	// text such as an error message or a connection string.
	keyValue = regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:password|passwd|pwd|secret|token|api[_-]?key|master[_-]?key|private[_-]?key)["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s&;,"']+)`)
)

// Redact removes the secrets this package can recognize inside free text:
// this product's tokens, bearer and basic credentials, passwords in URLs,
// and the values of password=..., token: ... and similar pairs. It is the
// last line of defence; code must still not put secrets into messages.
func Redact(s string) string {
	if s == "" {
		return s
	}
	s = productToken.ReplaceAllString(s, "${1}"+Redacted)
	s = bearer.ReplaceAllString(s, "${1} "+Redacted)
	s = urlPassword.ReplaceAllString(s, "${1}"+Redacted+"@")
	s = keyValue.ReplaceAllStringFunc(s, func(m string) string {
		sub := keyValue.FindStringSubmatch(m)
		value := sub[2]
		// Keep the quotes, so that redacted JSON is still JSON.
		if q := value[0]; (q == '"' || q == '\'') && len(value) >= 2 {
			return sub[1] + string(q) + Redacted + string(q)
		}
		return sub[1] + Redacted
	})
	return s
}

// RedactingHandler wraps another handler and removes secrets from every
// record before it is written: the message and every string, error and
// stringer value go through Redact, and the value of every attribute with a
// sensitive name (SensitiveKey) is replaced as a whole.
type RedactingHandler struct {
	next slog.Handler
}

// NewRedactingHandler wraps h.
func NewRedactingHandler(h slog.Handler) *RedactingHandler {
	return &RedactingHandler{next: h}
}

// Enabled implements slog.Handler.
func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *RedactingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, Redact(r.Message), r.PC)
	if id := RequestID(ctx); id != "" {
		out.AddAttrs(slog.String("request_id", id))
	}
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

// WithAttrs implements slog.Handler.
func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = redactAttr(a)
	}
	return &RedactingHandler{next: h.next.WithAttrs(clean)}
}

// WithGroup implements slog.Handler.
func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{next: h.next.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		group := a.Value.Group()
		clean := make([]slog.Attr, len(group))
		for i, g := range group {
			clean[i] = redactAttr(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}
	}
	if SensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Redact(a.Value.String()))
	case slog.KindAny:
		switch v := a.Value.Any().(type) {
		case error:
			return slog.String(a.Key, Redact(v.Error()))
		case interface{ String() string }:
			return slog.String(a.Key, Redact(v.String()))
		case []byte:
			return slog.String(a.Key, Redact(string(v)))
		case nil:
			return a
		default:
			// Structured values (maps, structs) are written by the JSON or
			// text encoder, which this handler cannot see into. Logging
			// them whole is not allowed: name the fields as attributes.
			return slog.String(a.Key, Redact(slog.AnyValue(v).String()))
		}
	}
	return a
}
