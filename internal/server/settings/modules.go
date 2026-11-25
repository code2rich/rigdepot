// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

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

func moduleChange(id, label string, enabled bool) (string, audit.Object) {
	action := ActionModuleDisable
	if enabled {
		action = ActionModuleEnable
	}
	return action, audit.Object{Kind: "module", ID: id, Label: label}
}

// AuthorizeModule checks that actor may turn the module on (or off), and
// records a refusal. The module host starts a module before it records it in
// the settings, so the server asks this first; SetModuleEnabled checks again.
func (s *Service) AuthorizeModule(ctx context.Context, actor audit.Actor, id, label string, enabled bool) error {
	action, object := moduleChange(id, label, enabled)
	return s.authorize(ctx, actor, action, object)
}

// SetModuleEnabled turns a module on or off in the settings and records it.
// Turning off keeps the module's data. label is the module's name, for the
// audit entry. Platform administrators only.
func (s *Service) SetModuleEnabled(ctx context.Context, actor audit.Actor, id, label string, enabled bool) error {
	action, object := moduleChange(id, label, enabled)
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

// A module's own settings are kept under modules.<module id>.<key>, as JSON
// values (ext/module.Settings). They are the module's business: no
// permission check and no audit entry here, since a module audits the changes
// it makes on someone's behalf itself.

// ErrNotModuleKey is the error for a key outside a module's settings.
var ErrNotModuleKey = errors.New("settings: not a module's setting")

func moduleKey(key string) error {
	rest, ok := strings.CutPrefix(key, "modules.")
	if !ok || key == keyModules {
		return fmt.Errorf("%w: %q", ErrNotModuleKey, key)
	}
	id, name, ok := strings.Cut(rest, ".")
	if !ok || id == "" || name == "" {
		return fmt.Errorf("%w: %q", ErrNotModuleKey, key)
	}
	return nil
}

// ModuleValue returns a module's setting; false when it was never set.
func (s *Service) ModuleValue(ctx context.Context, key string) (json.RawMessage, bool, error) {
	if err := moduleKey(key); err != nil {
		return nil, false, err
	}
	var v json.RawMessage
	found, err := getJSON(ctx, s.db, key, &v)
	if err != nil || !found {
		return nil, false, err
	}
	return v, true, nil
}

// SetModuleValue saves a module's setting, a JSON value.
func (s *Service) SetModuleValue(ctx context.Context, key string, value json.RawMessage) error {
	if err := moduleKey(key); err != nil {
		return err
	}
	if !json.Valid(value) {
		return fmt.Errorf("settings: the value of %s is not JSON", key)
	}
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		return put(ctx, tx, key, string(value), false, audit.Actor{}, s.now())
	})
}
