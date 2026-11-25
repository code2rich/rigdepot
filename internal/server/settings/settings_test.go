// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package settings_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/server/settings"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

var (
	admin  = audit.Actor{UserID: "0193a4b2-6c1e-7a00-8000-000000000001", Origin: audit.OriginWeb}
	member = audit.Actor{UserID: "0193a4b2-6c1e-7a00-8000-000000000011", Origin: audit.OriginWeb}
)

// onlyAdmin stands in for the access package: the administrator may do
// everything, nobody else anything.
func onlyAdmin(_ context.Context, a audit.Actor, _ string) (bool, error) {
	return a.UserID == admin.UserID, nil
}

type fixture struct {
	db   *dbx.DB
	keys *masterkey.Keyring
	log  *audit.Log
	s    *settings.Service
}

func setup(t *testing.T, db *dbx.DB, opts settings.Options) *fixture {
	t.Helper()
	ctx := context.Background()
	if _, err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	// The users the settings name as updated_by (a foreign key).
	for _, u := range []struct{ id, name string }{{admin.UserID, "admin"}, {member.UserID, "jdoe"}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users (id, username, status, created_at, updated_at) VALUES (?, ?, 'active', 1, 1)`, u.id, u.name); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := masterkey.New(bytes.Repeat([]byte{7}, masterkey.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	chain, err := settings.AuditKey(ctx, db, keys, true)
	if err != nil {
		t.Fatal(err)
	}
	log, err := audit.New(db, chain)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Authorize == nil {
		opts.Authorize = onlyAdmin
	}
	return &fixture{db: db, keys: keys, log: log, s: settings.New(db, keys, log, opts)}
}

func (f *fixture) entries(t *testing.T, action string) []audit.Row {
	t.Helper()
	page, err := f.log.Query(context.Background(), audit.Filter{Action: action}, 200, "")
	if err != nil {
		t.Fatal(err)
	}
	return page.Rows
}

// dump is every value in the settings table, to search for plaintext.
func (f *fixture) dump(t *testing.T) string {
	t.Helper()
	rows, err := f.db.QueryContext(context.Background(), `SELECT key, value FROM settings`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		b.WriteString(k + "=" + v + "\n")
	}
	return b.String()
}

func ptr[T any](v T) *T { return &v }

const password = "Zq8-mail-password-marker-4Kd9"

func TestPlatformSettings(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := setup(t, db, settings.Options{})
		ctx := context.Background()
		p, err := f.s.Platform(ctx, admin)
		if err != nil || p.OrganizationName != "" || p.Mail.Enabled || p.MailPasswordSet || p.ETag == "" {
			t.Fatalf("fresh settings: %+v %v", p, err)
		}

		if _, err := f.s.UpdatePlatform(ctx, admin, `"stale"`, settings.PlatformChange{OrganizationName: ptr("Example Corp")}); !errors.Is(err, settings.ErrPreconditionFailed) {
			t.Errorf("stale ETag: %v", err)
		}
		next, err := f.s.UpdatePlatform(ctx, admin, p.ETag, settings.PlatformChange{
			OrganizationName: ptr("  Example Corp "),
			Mail: &settings.MailChange{Enabled: ptr(true), Host: ptr("smtp.example.com"), Port: ptr(465),
				Username: ptr("rigdepot@example.com"), Password: ptr(password), From: ptr("rigdepot@example.com")},
		})
		if err != nil {
			t.Fatal(err)
		}
		want := settings.Mail{Enabled: true, Host: "smtp.example.com", Port: 465, Security: settings.SecurityTLS, Username: "rigdepot@example.com", From: "rigdepot@example.com"}
		if next.OrganizationName != "Example Corp" || next.Mail != want || !next.MailPasswordSet || next.ETag == p.ETag {
			t.Errorf("after the change: %+v", next)
		}
		// The ETag of the old version no longer works.
		if _, err := f.s.UpdatePlatform(ctx, admin, p.ETag, settings.PlatformChange{OrganizationName: ptr("Other")}); !errors.Is(err, settings.ErrPreconditionFailed) {
			t.Errorf("reusing an old ETag: %v", err)
		}

		// The mail sender gets the password; nothing stores it in the clear.
		server, err := f.s.MailServer(ctx)
		if err != nil || server.Password != password || server.Mail != want {
			t.Errorf("MailServer = %+v, %v", server.Mail, err)
		}
		if strings.Contains(f.dump(t), password) {
			t.Error("the password is in the settings table in the clear")
		}

		// Every change is audited, the password only as "replaced".
		rows := f.entries(t, settings.ActionSettingsUpdate)
		if len(rows) != 1 || rows[0].Actor != admin || rows[0].Result != audit.ResultOK {
			t.Fatalf("audit %+v", rows)
		}
		for _, want := range []string{`"mail.password":"replaced"`, `"organization.name":{"from":"","to":"Example Corp"}`, `"mail.host":{"from":"","to":"smtp.example.com"}`} {
			if !strings.Contains(rows[0].Change, want) {
				t.Errorf("audit change %s lacks %s", rows[0].Change, want)
			}
		}
		if strings.Contains(rows[0].Change, password) {
			t.Error("the password is in the audit log")
		}

		// Replacing the password with the same value is still a change; an
		// absent password stays; an empty one removes it.
		again, err := f.s.UpdatePlatform(ctx, admin, next.ETag, settings.PlatformChange{Mail: &settings.MailChange{Password: ptr(password)}})
		if err != nil || again.ETag == next.ETag {
			t.Errorf("same password again: %v, etag %s", err, again.ETag)
		}
		kept, err := f.s.UpdatePlatform(ctx, admin, again.ETag, settings.PlatformChange{Mail: &settings.MailChange{Port: ptr(587)}})
		if err != nil || !kept.MailPasswordSet || kept.Mail.Security != settings.SecurityTLS {
			t.Errorf("changing the port: %+v %v", kept, err)
		}
		gone, err := f.s.UpdatePlatform(ctx, admin, kept.ETag, settings.PlatformChange{Mail: &settings.MailChange{Password: ptr("")}})
		if err != nil || gone.MailPasswordSet {
			t.Errorf("removing the password: %+v %v", gone, err)
		}
		if rows := f.entries(t, settings.ActionSettingsUpdate); len(rows) != 4 || !strings.Contains(rows[0].Change, `"mail.password":"removed"`) {
			t.Errorf("audit after removing: %+v", rows)
		}

		// No change, no entry.
		if _, err := f.s.UpdatePlatform(ctx, admin, gone.ETag, settings.PlatformChange{OrganizationName: ptr("Example Corp")}); err != nil {
			t.Fatal(err)
		}
		if rows := f.entries(t, settings.ActionSettingsUpdate); len(rows) != 4 {
			t.Errorf("a change that changes nothing was audited: %d entries", len(rows))
		}
	})
}

func TestPlatformSettingsAreForAdministrators(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := setup(t, db, settings.Options{})
		ctx := context.Background()
		p, _ := f.s.Platform(ctx, admin)
		if _, err := f.s.Platform(ctx, member); !errors.Is(err, settings.ErrDenied) {
			t.Errorf("read by a member: %v", err)
		}
		if _, err := f.s.UpdatePlatform(ctx, member, p.ETag, settings.PlatformChange{OrganizationName: ptr("Mine")}); !errors.Is(err, settings.ErrDenied) {
			t.Errorf("change by a member: %v", err)
		}
		denied := f.entries(t, "settings.")
		if len(denied) != 2 || denied[0].Result != audit.ResultDenied || denied[0].Actor != member {
			t.Errorf("refusals are not audited: %+v", denied)
		}
		if name, _ := f.s.OrganizationName(ctx); name != "" {
			t.Error("a refused change was applied")
		}

		// Without an authorizer, nobody.
		none := settings.New(db, f.keys, f.log, settings.Options{})
		if _, err := none.Platform(ctx, admin); !errors.Is(err, settings.ErrDenied) {
			t.Errorf("no authorizer: %v", err)
		}
		// An authorizer that cannot decide refuses as well.
		broken := settings.New(db, f.keys, f.log, settings.Options{Authorize: func(context.Context, audit.Actor, string) (bool, error) {
			return true, errors.New("cannot reach the policy engine")
		}})
		if _, err := broken.Platform(ctx, admin); err == nil {
			t.Error("an authorizer error let the read through")
		}
	})
}

func TestInvalidMailSettingsAreRefused(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := setup(t, db, settings.Options{})
		ctx := context.Background()
		p, _ := f.s.Platform(ctx, admin)
		for field, c := range map[string]settings.MailChange{
			"mail.host":     {Enabled: ptr(true), Port: ptr(465), From: ptr("a@example.com")},
			"mail.from":     {Enabled: ptr(true), Host: ptr("smtp.example.com"), Port: ptr(465), From: ptr("not an address")},
			"mail.security": {Security: ptr("ssl")},
			"mail.port":     {Port: ptr(70000)},
		} {
			_, err := f.s.UpdatePlatform(ctx, admin, p.ETag, settings.PlatformChange{Mail: &c})
			var v *settings.ValidationError
			if !errors.As(err, &v) || v.Field != field {
				t.Errorf("%s: %v", field, err)
			}
		}
		if _, err := f.s.UpdatePlatform(ctx, admin, p.ETag, settings.PlatformChange{OrganizationName: ptr(strings.Repeat("x", 65))}); err == nil {
			t.Error("a 65-character name was accepted")
		}
		if rows := f.entries(t, settings.ActionSettingsUpdate); len(rows) != 0 {
			t.Errorf("refused changes were audited as changes: %+v", rows)
		}
	})
}

// TestFailedAuditFailsTheChange: a settings change is never committed
// without its audit entry.
func TestFailedAuditFailsTheChange(t *testing.T) {
	f := setup(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)), settings.Options{})
	ctx := context.Background()
	p, _ := f.s.Platform(ctx, admin)
	if _, err := f.db.ExecContext(ctx, `CREATE TRIGGER audit_log_full BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.UpdatePlatform(ctx, admin, p.ETag, settings.PlatformChange{OrganizationName: ptr("Example Corp")}); err == nil {
		t.Fatal("the change succeeded without its audit entry")
	}
	f.db.ExecContext(ctx, `DROP TRIGGER audit_log_full`)
	if name, _ := f.s.OrganizationName(ctx); name != "" {
		t.Errorf("the change was committed: %q", name)
	}
}

// TestSealedSettingMovedDoesNotOpen: a sealed value copied under another key
// is refused, not used.
func TestSealedSettingMovedDoesNotOpen(t *testing.T) {
	f := setup(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)), settings.Options{})
	ctx := context.Background()
	p, _ := f.s.Platform(ctx, admin)
	if _, err := f.s.UpdatePlatform(ctx, admin, p.ETag, settings.PlatformChange{Mail: &settings.MailChange{Password: ptr(password)}}); err != nil {
		t.Fatal(err)
	}
	// Put the audit chain key where the mail password goes.
	if _, err := f.db.ExecContext(ctx, `UPDATE settings SET value = (SELECT value FROM settings WHERE key = 'audit.key') WHERE key = 'mail.password'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.MailServer(ctx); !errors.Is(err, masterkey.ErrOpen) {
		t.Errorf("MailServer with a moved value: %v", err)
	}
}

func TestAuditKey(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		ctx := context.Background()
		if _, err := migrations.Apply(ctx, db); err != nil {
			t.Fatal(err)
		}
		keys, _ := masterkey.New(bytes.Repeat([]byte{1}, masterkey.KeySize))
		if has, err := settings.HasSealedData(ctx, db); err != nil || has {
			t.Fatalf("fresh database: %v %v", has, err)
		}
		if k, err := settings.AuditKey(ctx, db, keys, false); err != nil || k != nil {
			t.Fatalf("without create: %x %v", k, err)
		}
		k1, err := settings.AuditKey(ctx, db, keys, true)
		if err != nil || len(k1) != audit.KeySize {
			t.Fatalf("create: %x %v", k1, err)
		}
		k2, _ := settings.AuditKey(ctx, db, keys, true)
		if !bytes.Equal(k1, k2) {
			t.Error("a second call made another key")
		}
		if has, _ := settings.HasSealedData(ctx, db); !has {
			t.Error("HasSealedData is false with the audit key stored")
		}
		other, _ := masterkey.New(bytes.Repeat([]byte{2}, masterkey.KeySize))
		if _, err := settings.AuditKey(ctx, db, other, true); err == nil || !strings.Contains(err.Error(), "master key") {
			t.Errorf("another master key: %v", err)
		}
	})
}

// TestRotationKeepsSettingsAndTheChain: after "keys rotate" every sealed
// setting is under the new key, opens, and the audit chain key is the same,
// so the chain goes on unbroken.
func TestRotationKeepsSettingsAndTheChain(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := setup(t, db, settings.Options{})
		ctx := context.Background()
		p, _ := f.s.Platform(ctx, admin)
		if _, err := f.s.UpdatePlatform(ctx, admin, p.ETag, settings.PlatformChange{Mail: &settings.MailChange{Password: ptr(password)}}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.s.SetupToken(ctx); err != nil {
			t.Fatal(err)
		}
		before, _ := settings.AuditKey(ctx, db, f.keys, false)

		rotated, _ := masterkey.New(bytes.Repeat([]byte{9}, masterkey.KeySize), bytes.Repeat([]byte{7}, masterkey.KeySize))
		counts, err := masterkey.RewrapAll(ctx, db, rotated, []masterkey.Rewrapper{settings.Rewrapper})
		if err != nil || counts["settings"] != 3 {
			t.Fatalf("rewrapped %v, %v", counts, err)
		}
		if again, _ := masterkey.RewrapAll(ctx, db, rotated, []masterkey.Rewrapper{settings.Rewrapper}); again["settings"] != 0 {
			t.Errorf("a second run rewrapped %d", again["settings"])
		}
		newOnly, _ := masterkey.New(bytes.Repeat([]byte{9}, masterkey.KeySize))
		after, err := settings.AuditKey(ctx, db, newOnly, false)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("audit key after rotation: %v", err)
		}
		s := settings.New(db, newOnly, f.log, settings.Options{Authorize: onlyAdmin})
		if m, err := s.MailServer(ctx); err != nil || m.Password != password {
			t.Errorf("mail password after rotation: %v", err)
		}
		log, _ := audit.New(db, after)
		if _, err := log.Record(ctx, audit.Entry{Actor: audit.System, Action: "keys.rotate", Object: audit.Object{Kind: "master_key", ID: newOnly.CurrentID(), Label: "master key"}}); err != nil {
			t.Fatal(err)
		}
		if rep, err := log.Verify(ctx, 1, ""); err != nil || rep.Broken != nil {
			t.Errorf("chain after rotation: %+v %v", rep, err)
		}
	})
}

func TestModules(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := setup(t, db, settings.Options{})
		ctx := context.Background()
		if ids, err := f.s.EnabledModules(ctx); err != nil || len(ids) != 0 {
			t.Fatalf("fresh: %v %v", ids, err)
		}
		for _, step := range []struct {
			id string
			on bool
		}{{"insights", true}, {"envres", true}, {"insights", true}, {"insights", false}} {
			if err := f.s.SetModuleEnabled(ctx, admin, step.id, step.id, step.on); err != nil {
				t.Fatal(err)
			}
		}
		if ids, _ := f.s.EnabledModules(ctx); strings.Join(ids, ",") != "envres" {
			t.Errorf("enabled %v", ids)
		}
		if n := len(f.entries(t, "module.")); n != 3 {
			t.Errorf("%d module entries, want 3 (turning on what is on is no change)", n)
		}
		if err := f.s.SetModuleEnabled(ctx, member, "insights", "insights", true); !errors.Is(err, settings.ErrDenied) {
			t.Errorf("a member: %v", err)
		}
		if err := f.s.AuthorizeModule(ctx, admin, "insights", "insights", true); err != nil {
			t.Errorf("AuthorizeModule for the administrator: %v", err)
		}
		before := len(f.entries(t, "module."))
		if err := f.s.AuthorizeModule(ctx, member, "insights", "insights", false); !errors.Is(err, settings.ErrDenied) {
			t.Errorf("AuthorizeModule for a member: %v", err)
		}
		if got := f.entries(t, "module."); len(got) != before+1 || got[0].Result != audit.ResultDenied || got[0].Action != settings.ActionModuleDisable {
			t.Errorf("the refusal is not recorded: %+v", got)
		}
	})
}

func TestModuleValues(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := setup(t, db, settings.Options{})
		ctx := context.Background()
		if _, found, err := f.s.ModuleValue(ctx, "modules.insights.retention"); found || err != nil {
			t.Fatalf("never set: %v %v", found, err)
		}
		for _, v := range []string{`{"days":180}`, `90`} {
			if err := f.s.SetModuleValue(ctx, "modules.insights.retention", []byte(v)); err != nil {
				t.Fatal(err)
			}
			if got, found, err := f.s.ModuleValue(ctx, "modules.insights.retention"); !found || err != nil || string(got) != v {
				t.Errorf("read back %s: %s %v %v", v, got, found, err)
			}
		}
		for _, key := range []string{"modules.enabled", "mail", "modules.insights", "modules..x", "audit.key"} {
			if err := f.s.SetModuleValue(ctx, key, []byte(`1`)); !errors.Is(err, settings.ErrNotModuleKey) {
				t.Errorf("SetModuleValue(%q): %v", key, err)
			}
			if _, _, err := f.s.ModuleValue(ctx, key); !errors.Is(err, settings.ErrNotModuleKey) {
				t.Errorf("ModuleValue(%q): %v", key, err)
			}
		}
		if err := f.s.SetModuleValue(ctx, "modules.insights.x", []byte(`{`)); err == nil {
			t.Error("a value that is not JSON was saved")
		}
	})
}
