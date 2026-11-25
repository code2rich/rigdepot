// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package feedback

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The friction types a context may count (docs/subsystems/feedback-friction.md#摩擦).
var frictionTypes = map[string]bool{
	"tool_error": true, "waiting_approval": true, "repeated_correction": true, "missing_secret": true,
	"interrupted": true, "gate_denied": true, "sync_failed": true,
}

var (
	toolID       = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	syncResults  = map[string]bool{"ok": true, "partial": true, "failed": true}
	maxFrictions = 16
	maxCount     = 1_000_000
)

// Transport is the way feedback reached the server; it decides which context fields it may
// carry (docs/subsystems/feedback-friction.md#入口).
type transport int

const (
	viaWeb    transport = iota // the console: page, browser
	viaCLI                     // rgd feedback, also when an AI tool drafted it: versions, system, last sync, frictions
	viaSystem                  // the server itself: the rule that raised it
)

// cleanContext keeps the fields of c that feedback coming in by t may carry, and checks their
// values. Fields of another entry point are dropped, not refused: the contract promises that
// anything else is left out. A value that breaks a field's rule is refused.
func cleanContext(c Context, t transport) (Context, error) {
	var out Context
	var fields []FieldError
	text := func(field, v string, most int) string {
		v = strings.TrimSpace(v)
		switch {
		case utf8.RuneCountInString(v) > most:
			fields = append(fields, FieldError{Field: "context." + field, Code: "too_long"})
		case strings.ContainsFunc(v, unicode.IsControl):
			fields = append(fields, FieldError{Field: "context." + field, Code: "invalid"})
		}
		return v
	}
	switch t {
	case viaWeb:
		// Only the route: a query string or fragment could carry anything.
		page, _, _ := strings.Cut(c.Page, "?")
		page, _, _ = strings.Cut(page, "#")
		out.Page = text("page", page, 200)
		out.Browser = text("browser", c.Browser, 100)
	case viaCLI:
		out.ToolVersion = text("tool_version", c.ToolVersion, 64)
		out.CLIVersion = text("cli_version", c.CLIVersion, 32)
		out.OS = text("os", c.OS, 32)
		if c.Tool != "" {
			if !toolID.MatchString(c.Tool) {
				fields = append(fields, FieldError{Field: "context.tool", Code: "pattern"})
			}
			out.Tool = c.Tool
		}
		if c.LastSync != "" {
			if !syncResults[c.LastSync] {
				fields = append(fields, FieldError{Field: "context.last_sync", Code: "enum"})
			}
			out.LastSync = c.LastSync
		}
		if len(c.Frictions) > maxFrictions {
			fields = append(fields, FieldError{Field: "context.frictions", Code: "too_long"})
		}
		for i, f := range c.Frictions {
			if !frictionTypes[f.Type] {
				fields = append(fields, FieldError{Field: "context.frictions[" + strconv.Itoa(i) + "].type", Code: "enum"})
			}
			if f.Count < 1 || f.Count > maxCount {
				fields = append(fields, FieldError{Field: "context.frictions[" + strconv.Itoa(i) + "].count", Code: "range"})
			}
			out.Frictions = append(out.Frictions, FrictionCount{Type: f.Type, Count: f.Count})
		}
	case viaSystem:
		out.Trigger = text("trigger", c.Trigger, 200)
	}
	if len(fields) > 0 {
		return Context{}, invalid(fields...)
	}
	return out, nil
}
