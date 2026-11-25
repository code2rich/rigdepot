// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package settings_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// createAdmin stands in for access and authn: it inserts the account.
func createAdmin(id, name string) func(tx *dbx.Tx) (settings.NewAdmin, error) {
	return func(tx *dbx.Tx) (settings.NewAdmin, error) {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO users (id, username, status, is_admin, created_at, updated_at) VALUES (?, ?, 'active', TRUE, 1, 1)`, id, name)
		return settings.NewAdmin{UserID: id, Username: name}, err
	}
}

const firstAdminID = "0193a4b2-6c1e-7a00-8000-0000000000a1"

func TestSetupWizard(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := setup(t, db, settings.Options{})
		ctx := context.Background()

		st, err := f.s.Setup(ctx)
		if err != nil || st.AdminCreated || st.Complete {
			t.Fatalf("fresh: %+v %v", st, err)
		}
		token, generated, err := f.s.SetupToken(ctx)
		if err != nil || !generated || !regexp.MustCompile(`^[a-z2-7]{4}(-[a-z2-7]{4}){7}$`).MatchString(token) {
			t.Fatalf("token %q %v %v", token, generated, err)
		}
		// The same token on every start and every instance.
		again, _, _ := settings.New(db, f.keys, f.log, settings.Options{}).SetupToken(ctx)
		if again != token {
			t.Errorf("another instance got %q, want %q", again, token)
		}
		if strings.Contains(f.dump(t), token) {
			t.Error("the setup token is stored in the clear")
		}

		if _, err := f.s.CreateFirstAdmin(ctx, "wrong", createAdmin(firstAdminID, "root")); !errors.Is(err, settings.ErrSetupToken) {
			t.Errorf("wrong token: %v", err)
		}
		if denied := f.entries(t, settings.ActionSetupAdmin); len(denied) != 1 || denied[0].Result != audit.ResultDenied {
			t.Errorf("a wrong token is not audited: %+v", denied)
		}
		var created bool
		_, err = f.s.CreateFirstAdmin(ctx, token, func(tx *dbx.Tx) (settings.NewAdmin, error) {
			created = true
			return settings.NewAdmin{}, errors.New("password too short")
		})
		if err == nil || !created {
			t.Fatalf("a failing account creation: %v", err)
		}
		if st, _ := f.s.Setup(ctx); st.AdminCreated {
			t.Error("the step is marked done although the account was not created")
		}

		a, err := f.s.CreateFirstAdmin(ctx, " "+token+"\n", createAdmin(firstAdminID, "root"))
		if err != nil || a.UserID != firstAdminID {
			t.Fatalf("create: %+v %v", a, err)
		}
		if st, _ := f.s.Setup(ctx); !st.AdminCreated || st.Complete {
			t.Errorf("after creating: %+v", st)
		}
		if _, err := f.s.CreateFirstAdmin(ctx, token, createAdmin("0193a4b2-6c1e-7a00-8000-0000000000a2", "admin2")); !errors.Is(err, settings.ErrAdminExists) {
			t.Errorf("a second administrator: %v", err)
		}
		ok := f.entries(t, settings.ActionSetupAdmin)
		if len(ok) != 2 || ok[0].Result != audit.ResultOK || ok[0].Actor.UserID != firstAdminID || ok[0].Object.Label != "root" {
			t.Errorf("creation audit %+v", ok)
		}

		// Only an administrator closes it.
		if err := f.s.CompleteSetup(ctx, member); !errors.Is(err, settings.ErrDenied) {
			t.Errorf("complete by a member: %v", err)
		}
		if err := f.s.CompleteSetup(ctx, admin); err != nil {
			t.Fatal(err)
		}
		if st, _ := f.s.Setup(ctx); !st.Complete {
			t.Error("not complete")
		}

		// Closed for good: the token is gone and nothing in the group works.
		if tok, _, err := f.s.SetupToken(ctx); err != nil || tok != "" {
			t.Errorf("token after completion: %q %v", tok, err)
		}
		if strings.Contains(f.dump(t), "setup.token") {
			t.Error("the sealed setup token is still stored")
		}
		if _, err := f.s.CreateFirstAdmin(ctx, token, createAdmin("0193a4b2-6c1e-7a00-8000-0000000000a3", "late")); !errors.Is(err, settings.ErrSetupClosed) {
			t.Errorf("create after completion: %v", err)
		}
		if err := f.s.CompleteSetup(ctx, admin); !errors.Is(err, settings.ErrSetupClosed) {
			t.Errorf("complete twice: %v", err)
		}
		if n := len(f.entries(t, settings.ActionSetupComplete)); n != 2 {
			t.Errorf("%d completion entries, want the refusal and the completion", n)
		}
	})
}

func TestConfiguredSetupToken(t *testing.T) {
	f := setup(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)), settings.Options{SetupToken: "<placeholder-setup-token>"})
	ctx := context.Background()
	token, generated, err := f.s.SetupToken(ctx)
	if err != nil || generated || token != "<placeholder-setup-token>" {
		t.Fatalf("token %q %v %v", token, generated, err)
	}
	if strings.Contains(f.dump(t), "setup.token") {
		t.Error("a configured token was stored")
	}
	if _, err := f.s.CreateFirstAdmin(ctx, "<placeholder-setup-token>", createAdmin(firstAdminID, "root")); err != nil {
		t.Error(err)
	}
	if err := f.s.CompleteSetup(ctx, admin); err != nil {
		t.Fatal(err)
	}
	// Still set in the environment, but no longer accepted.
	if _, err := f.s.CreateFirstAdmin(ctx, "<placeholder-setup-token>", createAdmin("0193a4b2-6c1e-7a00-8000-0000000000a4", "x")); !errors.Is(err, settings.ErrSetupClosed) {
		t.Errorf("after completion: %v", err)
	}
}

func TestWrongSetupTokensAreLimited(t *testing.T) {
	f := setup(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)), settings.Options{})
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	f.s.SetClock(func() time.Time { return now })
	token, _, _ := f.s.SetupToken(ctx)
	for i := range 10 {
		if _, err := f.s.CreateFirstAdmin(ctx, "wrong", createAdmin(firstAdminID, "root")); !errors.Is(err, settings.ErrSetupToken) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := f.s.CreateFirstAdmin(ctx, token, createAdmin(firstAdminID, "root")); !errors.Is(err, settings.ErrSetupRateLimited) {
		t.Errorf("the right token right after ten wrong ones: %v", err)
	}
	now = now.Add(16 * time.Minute)
	if _, err := f.s.CreateFirstAdmin(ctx, token, createAdmin(firstAdminID, "root")); err != nil {
		t.Errorf("after the window: %v", err)
	}
}
