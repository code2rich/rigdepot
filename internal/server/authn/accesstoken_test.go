// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/authn"
)

func (e *env) browser(t *testing.T) authn.Caller {
	t.Helper()
	c, err := e.svc.Session(context.Background(), e.login(t, "admin", adminPassword).Token)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The acceptance check "the plain token is returned only once": it is in
// the response that issues it and nowhere else, not in the list, not in the
// database, not in the audit log.
func TestAccessTokenIsShownOnce(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	c := e.browser(t)
	plain, tok, err := e.svc.CreateAccessToken(ctx, c, authn.NewAccessToken{Name: " CI pipeline ", Scope: access.TokenRead, ExpiresInDays: 90})
	if err != nil {
		t.Fatal(err)
	}
	if !authn.TokenWellFormed(authn.AccessTokenPrefix, plain) || tok.Name != "CI pipeline" || tok.Prefix != plain[:len(authn.AccessTokenPrefix)+4] ||
		!tok.ExpiresAt.Equal(tok.CreatedAt.Add(90*24*time.Hour)) {
		t.Errorf("issued %q %+v", plain, tok)
	}
	list, err := e.svc.AccessTokens(ctx, c)
	if err != nil || len(list) != 1 || list[0].ID != tok.ID || list[0].Prefix != tok.Prefix {
		t.Fatalf("list: %+v %v", list, err)
	}
	secret := plain[len(tok.Prefix):]
	var n int
	e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_tokens WHERE token_digest LIKE ? OR token_prefix LIKE ? OR name LIKE ?`,
		"%"+secret+"%", "%"+secret+"%", "%"+secret+"%").Scan(&n)
	if n != 0 {
		t.Error("the token is stored in plain")
	}
	e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE change LIKE ?`, "%"+secret+"%").Scan(&n)
	if n != 0 {
		t.Error("the token is in the audit log")
	}
	if rows := e.entries(t, audit.Filter{Action: authn.ActionAccessTokenIssue}); len(rows) != 1 || rows[0].Object.ID != tok.ID {
		t.Errorf("audit: %+v", rows)
	}
}

func TestAccessTokenRules(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	c := e.browser(t)
	for _, n := range []authn.NewAccessToken{
		{Name: "", Scope: access.TokenRead, ExpiresInDays: 1},
		{Name: "x", Scope: "admin", ExpiresInDays: 1},
		{Name: "x", Scope: access.TokenRead, ExpiresInDays: 0},
		{Name: "x", Scope: access.TokenRead, ExpiresInDays: 367},
	} {
		if _, _, err := e.svc.CreateAccessToken(ctx, c, n); kindOf(err) != authn.KindInvalid {
			t.Errorf("%+v: %v", n, err)
		}
	}
	// Only from the browser.
	for _, other := range []authn.Caller{
		{Actor: access.Actor{UserID: e.admin.ID, Origin: access.OriginCLI, DeviceID: "0193a4b2-6c1e-7a00-8000-000000000201"}},
		{Actor: access.Actor{UserID: e.admin.ID, Origin: access.OriginCLI, TokenID: "0193a4b2-6c1e-7a00-8000-000000000301", Scope: access.TokenPublish}},
	} {
		if _, _, err := e.svc.CreateAccessToken(ctx, other, authn.NewAccessToken{Name: "x", Scope: access.TokenRead, ExpiresInDays: 1}); !errors.Is(err, authn.ErrDenied) {
			t.Errorf("issued to %+v: %v", other.Actor, err)
		}
	}

	plain, tok, err := e.svc.CreateAccessToken(ctx, c, authn.NewAccessToken{Name: "CI", Scope: access.TokenPublish, ExpiresInDays: 1})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.AccessToken(ctx, plain)
	if err != nil || got.Actor.UserID != e.admin.ID || got.Actor.TokenID != tok.ID || got.Actor.Scope != access.TokenPublish || got.Actor.Origin != access.OriginCLI {
		t.Fatalf("caller: %+v %v", got, err)
	}
	if list, _ := e.svc.AccessTokens(ctx, c); list[0].LastUsedAt == nil {
		t.Error("last use not recorded")
	}
	// Someone else cannot revoke it; its owner can, and it stops working.
	other := e.user(t, "jdoe", "<placeholder: jdoe password>")
	if err := e.svc.RevokeAccessToken(ctx, authn.Caller{Actor: access.Actor{UserID: other.ID, Origin: access.OriginWeb}}, tok.ID); !errors.Is(err, authn.ErrNotFound) {
		t.Errorf("revoked by someone else: %v", err)
	}
	if err := e.svc.RevokeAccessToken(ctx, c, tok.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RevokeAccessToken(ctx, c, tok.ID); err != nil {
		t.Errorf("revoking twice: %v", err)
	}
	if _, err := e.svc.AccessToken(ctx, plain); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Errorf("revoked token: %v", err)
	}
	if list, _ := e.svc.AccessTokens(ctx, c); len(list) != 0 {
		t.Errorf("revoked token listed: %+v", list)
	}

	// It expires.
	plain, _, _ = e.svc.CreateAccessToken(ctx, c, authn.NewAccessToken{Name: "short", Scope: access.TokenRead, ExpiresInDays: 1})
	e.clock.advance(24*time.Hour - time.Second)
	if _, err := e.svc.AccessToken(ctx, plain); err != nil {
		t.Errorf("before expiry: %v", err)
	}
	e.clock.advance(time.Second)
	if _, err := e.svc.AccessToken(ctx, plain); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Errorf("expired: %v", err)
	}
	// A device token or a broken one is not an access token.
	for _, bad := range []string{"", authn.NewToken(authn.DeviceTokenPrefix), plain[:len(plain)-1] + "0"} {
		if _, err := e.svc.AccessToken(ctx, bad); !errors.Is(err, authn.ErrUnauthenticated) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// The acceptance check "what a token may do is its scope intersected with
// what its owner may do at that moment".
func TestAccessTokenScopeAndOwner(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	c := e.browser(t)
	issue := func(scope access.TokenScope) access.Actor {
		plain, _, err := e.svc.CreateAccessToken(ctx, c, authn.NewAccessToken{Name: string(scope), Scope: scope, ExpiresInDays: 30})
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.svc.AccessToken(ctx, plain)
		if err != nil {
			t.Fatal(err)
		}
		return got.Actor
	}
	read, publish := issue(access.TokenRead), issue(access.TokenPublish)
	can := func(a access.Actor, action access.Action) bool {
		return e.access.Can(ctx, a, action, access.TargetOrg()).Allowed
	}
	// The owner, a platform administrator, may do all of these.
	for _, action := range []access.Action{access.ActionResourceRead, access.ActionResourcePublish, access.ActionSettingsManage} {
		if !can(c.Actor, action) {
			t.Fatalf("the administrator may not %s", action)
		}
	}
	for _, x := range []struct {
		a      access.Actor
		action access.Action
		want   bool
	}{
		{read, access.ActionResourceRead, true},
		{read, access.ActionResourcePublish, false}, // beyond the scope
		{publish, access.ActionResourcePublish, true},
		{publish, access.ActionSettingsManage, false}, // no token does this
	} {
		if got := can(x.a, x.action); got != x.want {
			t.Errorf("%s token, %s: %v, want %v", x.a.Scope, x.action, got, x.want)
		}
	}

	// Without the owner's rights, the token loses them at once.
	second := e.user(t, "second", "<placeholder: second password>")
	yes, no := true, false
	adminActor := access.Actor{UserID: e.admin.ID, Origin: access.OriginWeb}
	if _, err := e.access.UpdateUser(ctx, adminActor, second.ID, access.UserChange{Admin: &yes}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.access.UpdateUser(ctx, access.Actor{UserID: second.ID, Origin: access.OriginWeb}, e.admin.ID, access.UserChange{Admin: &no}); err != nil {
		t.Fatal(err)
	}
	if can(publish, access.ActionResourcePublish) {
		t.Error("the token still publishes in the organisation after its owner lost the right")
	}
}

// The real cost parameters make a hash this version can read back and that
// needs no upgrade; a hash with other parameters still verifies and is
// marked for an upgrade on the next sign-in.
func TestDefaultHashParameters(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, nil)
	var old string
	e.db.QueryRowContext(ctx, `SELECT password_hash FROM local_credentials WHERE user_id = ?`, e.admin.ID).Scan(&old)
	// newEnv hashed with the cheap parameters; a service with the defaults
	// accepts that password and replaces the hash.
	real := authn.New(e.db, authn.Options{Access: e.access, Audit: e.audit, Now: e.clock.now})
	if _, err := real.Login(ctx, "admin", adminPassword); err != nil {
		t.Fatal(err)
	}
	var upgraded string
	e.db.QueryRowContext(ctx, `SELECT password_hash FROM local_credentials WHERE user_id = ?`, e.admin.ID).Scan(&upgraded)
	if old == upgraded || upgraded[:len("$argon2id$v=19$m=19456,t=2,p=1$")] != "$argon2id$v=19$m=19456,t=2,p=1$" {
		t.Errorf("hash %q -> %q", old, upgraded)
	}
	if _, err := real.Login(ctx, "admin", adminPassword); err != nil {
		t.Fatal(err)
	}
	var again string
	e.db.QueryRowContext(ctx, `SELECT password_hash FROM local_credentials WHERE user_id = ?`, e.admin.ID).Scan(&again)
	if again != upgraded {
		t.Error("a current hash was replaced")
	}
	// A stored value that is not a hash is never a match.
	e.db.ExecContext(ctx, `UPDATE local_credentials SET password_hash = ? WHERE user_id = ?`, adminPassword, e.admin.ID)
	if _, err := real.Login(ctx, "admin", adminPassword); err == nil {
		t.Error("a plain stored password signed in")
	}
}
