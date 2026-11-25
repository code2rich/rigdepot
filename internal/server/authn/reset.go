// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn

import (
	"context"
	"strings"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// ActionPasswordReset is the audit action of "rgd-server reset-admin": an administrator's
// password set on the server itself, by whoever can run commands there.
const ActionPasswordReset = "auth.password.reset"

// ErrNotAdmin: the account named is not an active platform administrator.
var ErrNotAdmin = &Error{Kind: KindConflict, Code: "auth.not_admin"}

// Admins returns the active platform administrators, for choosing whose password to reset.
func (s *Service) Admins(ctx context.Context) ([]access.User, error) {
	all, err := s.access.ListUsers(ctx, access.System, access.UserFilter{Status: access.StatusActive})
	if err != nil {
		return nil, err
	}
	var out []access.User
	for _, u := range all {
		if u.Admin {
			out = append(out, u)
		}
	}
	return out, nil
}

// ResetAdminPassword sets the password of the active platform administrator with this account
// name (docs/architecture/06-server.md#本地账号: "forgot the administrator's password"). It is
// for the server's operator, not for requests: there is no caller to check, the access to the
// server is the permission. An account that signed in through an identity provider gets a
// local password as well, which is the way back in when the provider fails. The lock and the
// count of wrong passwords are cleared and every browser session of the account ends; the
// audit log records it with the system as the actor.
func (s *Service) ResetAdminPassword(ctx context.Context, username, password string) (access.User, error) {
	name := strings.ToLower(strings.TrimSpace(username))
	u, found, err := s.userByName(ctx, name)
	switch {
	case err != nil:
		return access.User{}, err
	case !found:
		return access.User{}, ErrNotFound
	case !u.Admin || u.Status != access.StatusActive:
		return access.User{}, ErrNotAdmin
	}
	if err := checkPassword("password", password, u.Username); err != nil {
		return access.User{}, err
	}
	hash, err := s.hasher.hash(ctx, password)
	if err != nil {
		return access.User{}, err
	}
	err = s.db.InTx(ctx, func(tx *dbx.Tx) error {
		_, had, err := readCredential(ctx, tx, u.ID)
		if err != nil {
			return err
		}
		if err := s.putCredential(ctx, tx, u.ID, hash, false); err != nil {
			return err
		}
		ended, err := s.endSessions(ctx, tx, u.ID, "")
		if err != nil {
			return err
		}
		change := map[string]any{"password": "replaced", "sessions_ended": ended}
		if !had {
			change["password"] = "created"
		}
		_, err = s.audit.Append(ctx, tx, audit.Entry{
			Actor: audit.System, Action: ActionPasswordReset, Object: userObject(u.ID, u.Username), Change: change,
		})
		return err
	})
	return u, err
}
