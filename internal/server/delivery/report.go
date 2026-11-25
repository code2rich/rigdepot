// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// The statuses of an install (contract: InstallStatus).
const (
	StatusInstalled = "installed"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
	StatusRemoved   = "removed"
)

// The results of a sync (contract: SyncResult).
const (
	ResultOK      = "ok"
	ResultPartial = "partial"
	ResultFailed  = "failed"
)

// DetectedTool is an AI tool the command line found, with the version it reported.
type DetectedTool struct {
	ID      string `json:"tool"`
	Version string `json:"version,omitempty"`
}

// InstallReport is what a sync left of one resource in one tool.
type InstallReport struct {
	Code     string
	Version  int
	Tool     string
	Status   string
	Degraded bool
	// Form is the form a degraded resource was delivered in, such as "skill".
	Form string
	// Reason is a stable code for skipped and failed, such as "name_taken".
	Reason string
}

// SyncReport is what the command line says after a sync.
type SyncReport struct {
	CLIVersion string
	OS, Arch   string
	FinishedAt time.Time
	Result     string
	Tools      []DetectedTool
	Installs   []InstallReport
}

// The limits of a report (contract: SyncReport).
const (
	maxTools    = 64
	maxInstalls = 4096
)

var (
	semverPattern  = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	toolPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	archPattern    = regexp.MustCompile(`^[a-z0-9_]{1,16}$`)
	formPattern    = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	reasonPattern  = regexp.MustCompile(`^[a-z0-9_.]{1,64}$`)
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z.+_ ()-]{1,64}$`)
)

// validate checks a report field by field. Only codes, versions and enumerations are accepted:
// a report never carries a path or text a person wrote, so the free-form fields (a tool's
// version, a reason) are held to patterns without slashes.
func (r SyncReport) validate() error {
	var fs []FieldError
	bad := func(field, code string) { fs = append(fs, FieldError{Field: field, Code: code}) }
	if !semverPattern.MatchString(r.CLIVersion) {
		bad("cli_version", "pattern")
	}
	if !slices.Contains([]string{"darwin", "linux", "windows"}, r.OS) {
		bad("os", "enum")
	}
	if !archPattern.MatchString(r.Arch) {
		bad("arch", "pattern")
	}
	if r.FinishedAt.IsZero() {
		bad("finished_at", "required")
	}
	if !slices.Contains([]string{ResultOK, ResultPartial, ResultFailed}, r.Result) {
		bad("result", "enum")
	}
	if len(r.Tools) > maxTools {
		bad("tools", "too_long")
	}
	for i, t := range r.Tools {
		if !toolPattern.MatchString(t.ID) {
			bad(fmt.Sprintf("tools[%d].tool", i), "pattern")
		}
		if t.Version != "" && !versionPattern.MatchString(t.Version) {
			bad(fmt.Sprintf("tools[%d].version", i), "pattern")
		}
	}
	if len(r.Installs) > maxInstalls {
		bad("installs", "too_long")
	}
	for i, in := range r.Installs {
		at := fmt.Sprintf("installs[%d].", i)
		if !resource.ValidCode(in.Code) {
			bad(at+"code", "pattern")
		}
		if in.Version < 1 {
			bad(at+"version", "minimum")
		}
		if !toolPattern.MatchString(in.Tool) {
			bad(at+"tool", "pattern")
		}
		if !slices.Contains([]string{StatusInstalled, StatusFailed, StatusSkipped, StatusRemoved}, in.Status) {
			bad(at+"status", "enum")
		}
		if in.Form != "" && !formPattern.MatchString(in.Form) {
			bad(at+"form", "pattern")
		}
		if in.Reason != "" && !reasonPattern.MatchString(in.Reason) {
			bad(at+"reason", "pattern")
		}
	}
	if len(fs) > 0 {
		return invalid(fs...)
	}
	return nil
}

// Report records what a sync left on the actor's computer: the computer's last sync (its
// command-line version, result and tools) and, per resource and tool, its install. Installs of
// resources the catalog does not have are ignored. Only a signed-in computer reports (a device
// token): anything else is ErrDenied.
func (s *Service) Report(ctx context.Context, a access.Actor, r SyncReport) error {
	if a.UserID == "" || a.DeviceID == "" || a.TokenID != "" {
		return ErrDenied
	}
	if err := r.validate(); err != nil {
		return err
	}
	codes := make([]string, 0, len(r.Installs))
	for _, in := range r.Installs {
		codes = append(codes, in.Code)
	}
	ids, err := s.catalog.ResourceIDs(ctx, codes)
	if err != nil {
		return err
	}
	tools := r.Tools
	if tools == nil {
		tools = []DetectedTool{}
	}
	toolsJSON, err := json.Marshal(tools)
	if err != nil {
		return err
	}
	now := s.millis()
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO device_syncs (device_id, user_id, cli_version, os, arch, result, tools, finished_at, reported_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (device_id) DO UPDATE SET user_id = excluded.user_id, cli_version = excluded.cli_version, os = excluded.os,
				arch = excluded.arch, result = excluded.result, tools = excluded.tools, finished_at = excluded.finished_at,
				reported_at = excluded.reported_at`,
			a.DeviceID, a.UserID, r.CLIVersion, r.OS, r.Arch, r.Result, string(toolsJSON), dbx.Millis(r.FinishedAt), now); err != nil {
			return err
		}
		for _, in := range r.Installs {
			id, ok := ids[in.Code]
			if !ok {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO installs (device_id, resource_id, tool, version, status, degraded, form, error_code, reported_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (device_id, resource_id, tool) DO UPDATE SET version = excluded.version, status = excluded.status,
					degraded = excluded.degraded, form = excluded.form, error_code = excluded.error_code, reported_at = excluded.reported_at`,
				a.DeviceID, id, in.Tool, in.Version, in.Status, in.Degraded, strings.TrimSpace(in.Form), in.Reason, now); err != nil {
				return err
			}
		}
		return nil
	})
}
