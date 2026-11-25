// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package delivery

import (
	"context"
	"database/sql"
	"errors"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/catalog"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Preferences returns, by resource id, what the person turned on (true) or off (false).
func (s *Service) Preferences(ctx context.Context, userID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT resource_id, enabled FROM user_resource_prefs WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var on bool
		if err := rows.Scan(&id, &on); err != nil {
			return nil, err
		}
		out[id] = on
	}
	return out, rows.Err()
}

// Preference is a person's choice about one resource, after it was stored.
type Preference struct {
	Code    string
	Enabled bool
	Policy  resource.Policy
}

// ActionPrefer is what a preference change is recorded as.
const ActionPrefer = access.ActionResourcePrefer

// SetPreference turns a published resource the actor sees on or off for themselves, on all
// their computers from their next sync. A required resource cannot be turned off
// (ErrRequired); turning it on changes nothing. A resource that is not published or not visible
// is ErrNotFound.
func (s *Service) SetPreference(ctx context.Context, a access.Actor, code string, on bool) (Preference, error) {
	r, _, err := s.catalog.Published(ctx, a, code)
	if errors.Is(err, catalog.ErrNotFound) {
		return Preference{}, ErrNotFound.with(map[string]any{"code": code})
	}
	if err != nil {
		return Preference{}, err
	}
	out := Preference{Code: r.Code, Enabled: on, Policy: r.Policy}
	if r.Policy == resource.PolicyRequired {
		if !on {
			return out, ErrRequired.with(map[string]any{"code": code})
		}
		return out, nil
	}
	target, _, err := s.catalog.Target(ctx, code)
	if err != nil {
		return out, err
	}
	obj := audit.Object{Kind: "resource", ID: r.Code, Label: r.Name}
	var denied *audit.Entry
	err = s.db.InTx(ctx, func(tx *dbx.Tx) error {
		d := s.access.CanIn(ctx, tx, a, ActionPrefer, target)
		if d.Err != nil {
			return d.Err
		}
		if !d.Allowed {
			denied = &audit.Entry{Actor: auditActor(a), Action: string(ActionPrefer), Object: obj, Result: audit.ResultDenied, Reason: d.Reason}
			return ErrDenied
		}
		var before *bool
		var was bool
		switch err := tx.QueryRowContext(ctx, `SELECT enabled FROM user_resource_prefs WHERE user_id = ? AND resource_id = ?`, a.UserID, r.ID).Scan(&was); {
		case err == nil:
			before = &was
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if before != nil && *before == on {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_resource_prefs (user_id, resource_id, enabled, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (user_id, resource_id) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`,
			a.UserID, r.ID, on, s.millis()); err != nil {
			return err
		}
		_, err := s.audit.Append(ctx, tx, audit.Entry{Actor: auditActor(a), Action: string(ActionPrefer), Object: obj,
			Change: map[string]any{"enabled": on}})
		return err
	})
	if denied != nil {
		if _, auditErr := s.audit.Record(ctx, *denied); auditErr != nil {
			return out, errors.Join(err, auditErr)
		}
	}
	return out, err
}
