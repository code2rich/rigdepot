// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"fmt"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// The access package and the audit log meet here. Neither may import the
// other's types the other way round (access → audit is allowed, but access
// chose an interface; audit must not know access), so the assembly
// translates.
//
// Action names: the audit log records access.Action values as they are
// (string(access.Action)), so for access's operations the audit's action
// names are access's enumeration. The audit package itself keeps actions as
// strings checked for their form, because packages that do not go through
// access (settings: settings.update, setup.*, module.*; the server:
// keys.rotate, audit.export) and optional modules record their own names.

// auditActor is an access.Actor as the audit log records it. The token and
// its scope are not recorded; the person is.
func auditActor(a access.Actor) audit.Actor {
	return audit.Actor{UserID: a.UserID, Origin: audit.Origin(a.Origin), ViaTool: a.ViaTool, DeviceID: a.DeviceID}
}

// accessActor is the reverse, for asking access about an actor the audit
// log describes. The operations that ask this way (settings, setup, audit)
// take a session or a device token, never an access token, so there is no
// token scope to carry over.
func accessActor(a audit.Actor) access.Actor {
	return access.Actor{UserID: a.UserID, Origin: access.Origin(a.Origin), ViaTool: a.ViaTool, DeviceID: a.DeviceID}
}

// accessAuditor is access.Auditor on the audit log: access's entries go into
// the transaction access hands over, in the platform category.
type accessAuditor struct{ log *audit.Log }

var _ access.Auditor = accessAuditor{}

func (a accessAuditor) Append(ctx context.Context, tx *dbx.Tx, e access.AuditEntry) error {
	_, err := a.log.Append(ctx, tx, audit.Entry{
		Category: audit.CategoryPlatform,
		Actor:    auditActor(e.Actor),
		Action:   string(e.Action),
		Object:   audit.Object{Kind: e.ObjectKind, ID: e.ObjectID, Label: e.ObjectLabel},
		Change:   e.Change,
		Result:   audit.Result(e.Result),
		Reason:   e.Reason,
	})
	return err
}

// accessAuthorizer answers hooks.authorize with access's rules. The actions
// of settings and the audit page map onto access's: everything about
// settings, the wizard, modules, the tool switches and the minimum
// command-line version is settings.manage on the platform;
// reading the whole audit log is audit.read on the platform.
func accessAuthorizer(svc *access.Service) func(ctx context.Context, a audit.Actor, action string, object audit.Object) (bool, error) {
	return func(ctx context.Context, a audit.Actor, action string, object audit.Object) (bool, error) {
		var act access.Action
		switch action {
		case settings.ActionSettingsRead, settings.ActionSettingsUpdate, settings.ActionSetupComplete,
			settings.ActionModuleEnable, settings.ActionModuleDisable,
			settings.ActionAdapterEnable, settings.ActionAdapterDisable, settings.ActionCLIPolicyUpdate:
			act = access.ActionSettingsManage
		case actionAuditRead:
			if object != (audit.Object{}) {
				// The log of one resource for its maintainers needs the
				// resource's maintainers from catalog. TODO(M1-24).
				return false, nil
			}
			act = access.ActionAuditRead
		default:
			return false, fmt.Errorf("no permission rule maps action %q", action)
		}
		d := svc.Can(ctx, accessActor(a), act, access.TargetPlatform())
		return d.Allowed, d.Err
	}
}

// accessNames is auditNames from the users access keeps. Devices come with
// the device code flow (authn, M1-22): until then their ids stand in.
type accessNames struct{ svc *access.Service }

func (n accessNames) User(ctx context.Context, id string) (string, string, bool) {
	u, err := n.svc.GetUser(ctx, access.System, id)
	if err != nil {
		return "", "", false
	}
	return u.Username, u.DisplayName, true
}

func (accessNames) Device(context.Context, string) (string, bool) { return "", false }
