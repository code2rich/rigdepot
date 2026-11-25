// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package settings

import (
	"context"
	"slices"

	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// The module actions, also the audit's.
const (
	ActionModuleEnable  = "module.enable"
	ActionModuleDisable = "module.disable"
)

// EnabledModules returns the ids of the optional modules turned on in the
// settings, sorted. Whether a module is compiled in, or disabled by the
// deployment (RGD_MODULES_DISABLED), is the module host's to decide.
func (s *Service) EnabledModules(ctx context.Context) ([]string, error) {
	return enabledModules(ctx, s.db)
}

func enabledModules(ctx context.Context, q dbx.Querier) ([]string, error) {
	var ids []string
	if _, err := getJSON(ctx, q, keyModules, &ids); err != nil {
		return nil, err
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// SetModuleEnabled turns a module on or off in the settings and records it.
// Turning off keeps the module's data. label is the module's name, for the
// audit entry. Platform administrators only.
func (s *Service) SetModuleEnabled(ctx context.Context, actor audit.Actor, id, label string, enabled bool) error {
	action := ActionModuleDisable
	if enabled {
		action = ActionModuleEnable
	}
	object := audit.Object{Kind: "module", ID: id, Label: label}
	if err := s.authorize(ctx, actor, action, object); err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if err := tx.Lock(ctx, lockKey); err != nil {
			return err
		}
		ids, err := enabledModules(ctx, tx)
		if err != nil {
			return err
		}
		if slices.Contains(ids, id) == enabled {
			return nil // already so: nothing changed, nothing to record
		}
		if enabled {
			ids = append(ids, id)
			slices.Sort(ids)
		} else {
			ids = slices.DeleteFunc(ids, func(m string) bool { return m == id })
		}
		if ids == nil {
			ids = []string{}
		}
		if err := putJSON(ctx, tx, keyModules, ids, actor, s.now()); err != nil {
			return err
		}
		_, err = s.log.Append(ctx, tx, audit.Entry{Actor: actor, Action: action, Object: object})
		return err
	})
}
