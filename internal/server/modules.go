// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/code2rich/rigdepot/ext/module"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/modhost"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// The module host (modhost) asks for settings, audit and permissions through small interfaces
// of its own; the domain packages that provide them meet it here.

// moduleSettings is modhost.SettingsStore on the settings package. The list of modules that are
// on is settings.EnabledModules; changing it goes through SetModuleEnabled, as the person in
// the request's context (access.ActorFrom), so that every switch is checked and audited. A
// module's own settings are settings.ModuleValue.
type moduleSettings struct {
	settings *settings.Service
	// label names a module for the audit entry.
	label func(id string) string
}

var _ modhost.SettingsStore = moduleSettings{}

func (m moduleSettings) Get(ctx context.Context, key string) (json.RawMessage, bool, error) {
	if key != modhost.EnabledKey {
		return m.settings.ModuleValue(ctx, key)
	}
	ids, err := m.settings.EnabledModules(ctx)
	if err != nil {
		return nil, false, err
	}
	if ids == nil {
		ids = []string{}
	}
	raw, err := json.Marshal(ids)
	return raw, true, err
}

// errNoActor refuses a module switch that nobody asked for: the audit log must say who.
var errNoActor = errors.New("a module is turned on or off on someone's behalf, and the request names nobody")

func (m moduleSettings) Set(ctx context.Context, key string, value json.RawMessage) error {
	if key != modhost.EnabledKey {
		return m.settings.SetModuleValue(ctx, key, value)
	}
	var want []string
	if err := json.Unmarshal(value, &want); err != nil {
		return fmt.Errorf("%s is not a list of module IDs: %w", key, err)
	}
	a, ok := access.ActorFrom(ctx)
	if !ok || a.UserID == "" {
		return errNoActor
	}
	actor := auditActor(a)
	have, err := m.settings.EnabledModules(ctx)
	if err != nil {
		return err
	}
	for _, id := range have {
		if !slices.Contains(want, id) {
			if err := m.settings.SetModuleEnabled(ctx, actor, id, m.label(id), false); err != nil {
				return err
			}
		}
	}
	for _, id := range want {
		if !slices.Contains(have, id) {
			if err := m.settings.SetModuleEnabled(ctx, actor, id, m.label(id), true); err != nil {
				return err
			}
		}
	}
	return nil
}

// moduleAuditor is modhost.AuditAppender on the audit log: a module's entries go into the
// module's own transaction, in the platform category. The host has already checked that the
// action starts with the module's ID.
type moduleAuditor struct{ log *audit.Log }

var _ modhost.AuditAppender = moduleAuditor{}

func (m moduleAuditor) Append(ctx context.Context, tx *dbx.Tx, a module.Actor, e module.AuditEntry) error {
	entry := audit.Entry{
		Category: audit.CategoryPlatform,
		Actor:    audit.Actor{UserID: a.UserID, Origin: audit.Origin(a.Origin), ViaTool: a.ViaTool, DeviceID: a.DeviceID},
		Action:   e.Action,
		Object:   audit.Object{Kind: e.ObjectKind, ID: e.ObjectID, Label: e.ObjectLabel},
		Change:   e.Change,
		Reason:   e.Reason,
	}
	if e.Denied {
		entry.Result = audit.ResultDenied
	}
	_, err := m.log.Append(ctx, tx, entry)
	return err
}

// moduleAuthz is module.Authorizer on access. A module's actions are decided by access's
// action table like everyone else's: an action access does not list is refused, so a module
// can do nothing on anyone's behalf until its actions are added there (the first real module,
// M2).
type moduleAuthz struct{ access *access.Service }

var _ module.Authorizer = moduleAuthz{}

func (m moduleAuthz) Actor(ctx context.Context) (module.Actor, bool) {
	a, ok := access.ActorFrom(ctx)
	if !ok || a.UserID == "" {
		return module.Actor{}, false
	}
	return module.Actor{UserID: a.UserID, Origin: string(a.Origin), ViaTool: a.ViaTool, DeviceID: a.DeviceID, TokenID: a.TokenID}, true
}

func (m moduleAuthz) Allowed(ctx context.Context, a module.Actor, action string, t module.Target) (bool, error) {
	if a.UserID == "" {
		// Not the server itself: a module could name it as easily as anyone.
		return false, nil
	}
	var target access.Target
	switch t.Kind {
	case "", "platform":
		target = access.TargetPlatform()
	case "product":
		target = access.TargetProduct(t.ID)
	case "account", "user":
		target = access.TargetAccount(t.ID)
	default:
		return false, fmt.Errorf("modules cannot ask about a target of kind %q yet", t.Kind)
	}
	// The token's scope is not carried by module.Actor: take it from the request when it is
	// the same person, so that a read-only token stays read-only.
	actor := access.Actor{UserID: a.UserID, Origin: access.Origin(a.Origin), ViaTool: a.ViaTool, DeviceID: a.DeviceID, TokenID: a.TokenID}
	if r, ok := access.ActorFrom(ctx); ok && r.UserID == a.UserID && r.TokenID == a.TokenID {
		actor.Scope = r.Scope
	}
	d := m.access.Can(ctx, actor, access.Action(action), target)
	return d.Allowed, d.Err
}
