// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn_test

import (
	"context"
	"errors"
	"testing"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/authn"
)

// TestResetAdminPassword is "forgot the administrator's password": the new password works, the
// old one does not, a lock is lifted, the sessions end, and the audit log has it.
func TestResetAdminPassword(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	in := e.login(t, "admin", adminPassword)
	for range 10 {
		e.svc.Login(ctx, "admin", "wrong")
	}
	if _, err := e.svc.Login(ctx, "admin", adminPassword); kindOf(err) != authn.KindLocked {
		t.Fatalf("not locked: %v", err)
	}
	const next = "<placeholder: new admin password>"
	u, err := e.svc.ResetAdminPassword(ctx, " Admin ", next)
	if err != nil || u.ID != e.admin.ID {
		t.Fatalf("reset: %v %+v", err, u)
	}
	e.login(t, "admin", next)
	if _, err := e.svc.Login(ctx, "admin", adminPassword); !errors.Is(err, authn.ErrInvalidCredentials) {
		t.Errorf("the old password: %v", err)
	}
	if _, err := e.svc.Session(ctx, in.Token); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Errorf("the session from before the reset: %v", err)
	}
	rows := e.entries(t, audit.Filter{Action: authn.ActionPasswordReset})
	if len(rows) != 1 || rows[0].Actor.Origin != audit.OriginSystem || rows[0].Object.ID != e.admin.ID ||
		string(rows[0].Change) != `{"password":"replaced","sessions_ended":1}` {
		t.Errorf("audit: %+v", rows)
	}
}

func TestResetAdminPasswordRefusals(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	e.user(t, "someone", "<placeholder: someone's password>")
	tests := []struct {
		name, user, password string
		want                 error
	}{
		{"unknown account", "nobody", "<placeholder: long password>", authn.ErrNotFound},
		{"not an administrator", "someone", "<placeholder: long password>", authn.ErrNotAdmin},
		{"too short", "admin", "short", nil},
		{"the account name", "admin", "admin", nil},
	}
	for _, tt := range tests {
		_, err := e.svc.ResetAdminPassword(ctx, tt.user, tt.password)
		switch {
		case tt.want != nil && !errors.Is(err, tt.want):
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		case tt.want == nil && kindOf(err) != authn.KindInvalid:
			t.Errorf("%s: %v, want an invalid password", tt.name, err)
		}
	}
	// A disabled administrator is not reset.
	by := access.Actor{UserID: e.admin.ID, Origin: access.OriginWeb}
	yes, disabled := true, access.StatusDisabled
	other := e.user(t, "other", "<placeholder: other's password>")
	if _, err := e.access.UpdateUser(ctx, by, other.ID, access.UserChange{Admin: &yes}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.access.UpdateUser(ctx, by, other.ID, access.UserChange{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ResetAdminPassword(ctx, "other", "<placeholder: long password>"); !errors.Is(err, authn.ErrNotAdmin) {
		t.Errorf("disabled administrator: %v", err)
	}
	if admins, err := e.svc.Admins(ctx); err != nil || len(admins) != 1 || admins[0].ID != e.admin.ID {
		t.Errorf("Admins: %+v %v", admins, err)
	}
	e.login(t, "someone", "<placeholder: someone's password>")
	if rows := e.entries(t, audit.Filter{Action: authn.ActionPasswordReset}); len(rows) != 0 {
		t.Errorf("refused resets in the audit log: %+v", rows)
	}
}
