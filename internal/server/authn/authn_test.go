// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// cheap keeps the tests fast; TestDefaultHashParameters covers the real
// ones.
var cheap = authn.HashParams{Memory: 64, Time: 1, Threads: 1}

// accessAuditor puts access's entries into the real audit log, like the
// server's assembly does.
type accessAuditor struct{ log *audit.Log }

func (a accessAuditor) Append(ctx context.Context, tx *dbx.Tx, e access.AuditEntry) error {
	_, err := a.log.Append(ctx, tx, audit.Entry{
		Actor:  audit.Actor{UserID: e.Actor.UserID, Origin: audit.Origin(e.Actor.Origin)},
		Action: string(e.Action), Object: audit.Object{Kind: e.ObjectKind, ID: e.ObjectID, Label: e.ObjectLabel},
		Change: e.Change, Result: audit.Result(e.Result), Reason: e.Reason,
	})
	return err
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type devices struct {
	byToken map[string][2]string
	out     map[string]bool
}

func (d *devices) VerifyDeviceToken(_ context.Context, token string) (string, string, error) {
	v, ok := d.byToken[token]
	if !ok || d.out[v[1]] {
		return "", "", authn.ErrUnauthenticated
	}
	return v[0], v[1], nil
}

func (d *devices) SignOutDevices(_ context.Context, _ *dbx.Tx, userID string) (int, error) {
	n := 0
	for _, v := range d.byToken {
		if v[0] == userID && !d.out[v[1]] {
			d.out[v[1]] = true
			n++
		}
	}
	return n, nil
}

type env struct {
	db     *dbx.DB
	access *access.Service
	audit  *audit.Log
	svc    *authn.Service
	clock  *clock
	admin  access.User
}

const adminPassword = "<placeholder: admin password>"

// newEnv is a fresh database with the first administrator, "admin", created
// the way the setup wizard does.
func newEnv(t *testing.T, devs authn.DeviceTokens) *env {
	t.Helper()
	ctx := context.Background()
	db := dbxtest.Open(t, dbxtest.SQLiteURL(t))
	if _, err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	log, err := audit.New(db, make([]byte, audit.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)}
	acc := access.New(db, access.Options{Auditor: accessAuditor{log}})
	e := &env{db: db, access: acc, audit: log, clock: c,
		svc: authn.New(db, authn.Options{Access: acc, Audit: log, Devices: devs, Hash: cheap, Now: c.now})}
	err = db.InTx(ctx, func(tx *dbx.Tx) error {
		e.admin, err = e.svc.CreateFirstAdmin(ctx, tx, authn.FirstAdmin{Username: "admin", DisplayName: "Administrator", Password: adminPassword})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// user adds an active account with a password, through an invitation.
func (e *env) user(t *testing.T, name, password string) access.User {
	t.Helper()
	ctx := context.Background()
	invs, err := e.access.CreateInvites(ctx, access.Actor{UserID: e.admin.ID, Origin: access.OriginWeb}, []string{name + "@example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	in, err := e.svc.AcceptInvite(ctx, invs[0].Token, access.NewAccount{Username: name, DisplayName: "Name of " + name}, password)
	if err != nil {
		t.Fatal(err)
	}
	return in.User
}

func (e *env) login(t *testing.T, name, password string) authn.SignedIn {
	t.Helper()
	in, err := e.svc.Login(context.Background(), name, password)
	if err != nil {
		t.Fatalf("login %s: %v", name, err)
	}
	return in
}

func (e *env) entries(t *testing.T, f audit.Filter) []audit.Row {
	t.Helper()
	page, err := e.audit.Query(context.Background(), f, audit.MaxLimit, "")
	if err != nil {
		t.Fatal(err)
	}
	return page.Rows
}

func kindOf(err error) authn.Kind {
	var e *authn.Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return 0
}

func fieldOf(err error) string {
	var e *authn.Error
	if errors.As(err, &e) && len(e.Fields) > 0 {
		return e.Fields[0].Field + ":" + e.Fields[0].Code
	}
	return ""
}

// The acceptance check "no default password": a fresh database has no
// password anyone could sign in with, and the first administrator's has to
// be chosen and be long enough.
func TestNoDefaultPassword(t *testing.T) {
	ctx := context.Background()
	db := dbxtest.Open(t, dbxtest.SQLiteURL(t))
	if _, err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM local_credentials`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a fresh database has %d passwords (%v)", n, err)
	}
	log, _ := audit.New(db, make([]byte, audit.KeySize))
	acc := access.New(db, access.Options{Auditor: accessAuditor{log}})
	svc := authn.New(db, authn.Options{Access: acc, Audit: log, Hash: cheap})
	for _, name := range []string{"admin", "root", "administrator"} {
		for _, pw := range []string{"", "admin", "password", "changeme"} {
			if _, err := svc.Login(ctx, name, pw); !errors.Is(err, authn.ErrInvalidCredentials) {
				t.Errorf("login %s/%q on a fresh database: %v", name, pw, err)
			}
		}
	}
	for _, c := range []struct{ pw, want string }{
		{"", "password:required"},
		{"short-pw", "password:too_short"},
		{"admin-admin", "password:too_short"},
		{"   Admin   x", ""}, // 12 characters, not only the name
		{"ADMIN" + strings.Repeat(" ", 10), "password:same_as_username"},
		{strings.Repeat("x", authn.MaxPasswordLength+1), "password:too_long"},
	} {
		err := db.InTx(ctx, func(tx *dbx.Tx) error {
			_, err := svc.CreateFirstAdmin(ctx, tx, authn.FirstAdmin{Username: "admin", Password: c.pw})
			if err == nil {
				return errors.New("rolled back on purpose")
			}
			return err
		})
		if got := fieldOf(err); got != c.want {
			t.Errorf("first administrator with %q: %v, want %s", c.pw, err, c.want)
		}
	}
}

func TestLoginStartsASession(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	in := e.login(t, "  Admin ", adminPassword)
	if in.Token == "" || in.User.ID != e.admin.ID || !in.User.Admin {
		t.Fatalf("signed in: %+v", in)
	}
	c, err := e.svc.Session(ctx, in.Token)
	if err != nil || c.Actor.UserID != e.admin.ID || c.Actor.Origin != access.OriginWeb || c.SessionID != in.SessionID {
		t.Fatalf("session: %+v %v", c, err)
	}
	for _, bad := range []string{"", "x", in.Token + "x", strings.Repeat("a", 500)} {
		if _, err := e.svc.Session(ctx, bad); !errors.Is(err, authn.ErrUnauthenticated) {
			t.Errorf("session %q: %v", bad, err)
		}
	}
	// The database has the digest only.
	var n int
	e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE token_digest = ?`, in.Token).Scan(&n)
	if n != 0 {
		t.Error("the session token is stored in plain")
	}
	if err := e.svc.Logout(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Session(ctx, in.Token); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Errorf("after logout: %v", err)
	}
	if rows := e.entries(t, audit.Filter{Category: audit.CategoryLogin}); len(rows) != 2 ||
		rows[0].Action != authn.ActionLogout || rows[1].Action != authn.ActionLogin || rows[1].Method != "local" || rows[1].Actor.UserID != e.admin.ID {
		t.Errorf("login entries: %+v", rows)
	}
}

// The acceptance check "failed sign-ins are in the audit log": wrong
// passwords and unknown accounts get the same answer, and both are recorded
// without the password.
func TestFailedLoginsAreAudited(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	const wrong = "<placeholder: wrong-password-marker>"
	if _, err := e.svc.Login(ctx, "admin", wrong); !errors.Is(err, authn.ErrInvalidCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	if _, err := e.svc.Login(ctx, "nobody", wrong); !errors.Is(err, authn.ErrInvalidCredentials) {
		t.Errorf("unknown account: %v", err)
	}
	rows := e.entries(t, audit.Filter{Category: audit.CategoryLogin, Result: audit.ResultDenied})
	if len(rows) != 2 {
		t.Fatalf("denied sign-ins: %+v", rows)
	}
	unknown, bad := rows[0], rows[1]
	if bad.Object.ID != e.admin.ID || bad.Reason != "invalid_credentials" || bad.Actor.UserID != "" || bad.Method != "local" || bad.Actor.Origin != audit.OriginWeb {
		t.Errorf("wrong password entry: %+v", bad)
	}
	if unknown.Object.ID != "" || unknown.Object.Label != "nobody" || unknown.Reason != "unknown_account" {
		t.Errorf("unknown account entry: %+v", unknown)
	}
	var n int
	e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE change LIKE ? OR object_label LIKE ? OR reason LIKE ?`,
		"%marker%", "%marker%", "%marker%").Scan(&n)
	if n != 0 {
		t.Error("a password reached the audit log")
	}
}

func TestWrongPasswordsLockTheAccount(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if _, err := e.svc.Login(ctx, "admin", "wrong password "+string(rune('a'+i))); !errors.Is(err, authn.ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Three are free; the fourth locks for a second.
	e.login(t, "admin", adminPassword) // and a right password resets the count
	for i := 1; i <= 4; i++ {
		e.svc.Login(ctx, "admin", "wrong")
	}
	_, err := e.svc.Login(ctx, "admin", adminPassword)
	var le *authn.Error
	if !errors.As(err, &le) || le.Kind != authn.KindLocked || le.RetryAfter != time.Second {
		t.Fatalf("locked after 4: %v", err)
	}
	e.clock.advance(time.Second)
	e.login(t, "admin", adminPassword)

	// Ten in a row lock for a quarter of an hour; meanwhile even the right
	// password is refused, without being checked.
	for i := 1; i <= 10; i++ {
		e.svc.Login(ctx, "admin", "wrong")
		e.clock.advance(time.Minute)
	}
	if _, err := e.svc.Login(ctx, "admin", adminPassword); kindOf(err) != authn.KindLocked {
		t.Fatalf("after 10: %v", err)
	}
	e.clock.advance(13 * time.Minute)
	if _, err := e.svc.Login(ctx, "admin", adminPassword); kindOf(err) != authn.KindLocked {
		t.Fatalf("after 13 more minutes: %v", err)
	}
	e.clock.advance(time.Minute)
	e.login(t, "admin", adminPassword)
	if !strings.Contains(e.reasons(t), "locked") {
		t.Errorf("lock not in the audit log: %s", e.reasons(t))
	}
}

func (e *env) reasons(t *testing.T) string {
	var out []string
	for _, r := range e.entries(t, audit.Filter{Category: audit.CategoryLogin, Result: audit.ResultDenied}) {
		out = append(out, r.Reason)
	}
	return strings.Join(out, ",")
}

// The acceptance check "a disabled account is refused with its next
// request": sessions and access tokens stop working at once, and the
// password says so only when it is right.
func TestDisablingTakesEffectWithTheNextRequest(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	u := e.user(t, "jdoe", "<placeholder: jdoe password>")
	in := e.login(t, "jdoe", "<placeholder: jdoe password>")
	c, err := e.svc.Session(ctx, in.Token)
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := e.svc.CreateAccessToken(ctx, c, authn.NewAccessToken{Name: "CI", Scope: access.TokenRead, ExpiresInDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AccessToken(ctx, plain); err != nil {
		t.Fatal(err)
	}
	disabled := access.StatusDisabled
	if _, err := e.access.UpdateUser(ctx, access.Actor{UserID: e.admin.ID, Origin: access.OriginWeb}, u.ID, access.UserChange{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Session(ctx, in.Token); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Errorf("session of a disabled account: %v", err)
	}
	if _, err := e.svc.AccessToken(ctx, plain); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Errorf("token of a disabled account: %v", err)
	}
	if _, err := e.svc.Login(ctx, "jdoe", "wrong"); !errors.Is(err, authn.ErrInvalidCredentials) {
		t.Errorf("wrong password of a disabled account: %v", err)
	}
	if _, err := e.svc.Login(ctx, "jdoe", "<placeholder: jdoe password>"); !errors.Is(err, authn.ErrDisabled) {
		t.Errorf("right password of a disabled account: %v", err)
	}
	active := access.StatusActive
	if _, err := e.access.UpdateUser(ctx, access.Actor{UserID: e.admin.ID, Origin: access.OriginWeb}, u.ID, access.UserChange{Status: &active}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Session(ctx, in.Token); err != nil {
		t.Errorf("session after enabling again: %v", err)
	}
}

func TestSessionsEnd(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	in := e.login(t, "admin", adminPassword)
	// Used every six days, it lasts thirty.
	for range 4 {
		e.clock.advance(6 * 24 * time.Hour)
		if _, err := e.svc.Session(ctx, in.Token); err != nil {
			t.Fatalf("after %v: %v", e.clock.now().Sub(in.ExpiresAt.Add(-authn.SessionMax)), err)
		}
	}
	e.clock.advance(6 * 24 * time.Hour)
	if _, err := e.svc.Session(ctx, in.Token); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Errorf("after 30 days: %v", err)
	}
	// Seven days unused end it.
	in = e.login(t, "admin", adminPassword)
	e.clock.advance(authn.SessionIdle - time.Second)
	if _, err := e.svc.Session(ctx, in.Token); err != nil {
		t.Errorf("just under idle: %v", err)
	}
	e.clock.advance(authn.SessionIdle)
	if _, err := e.svc.Session(ctx, in.Token); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Errorf("idle: %v", err)
	}
	if err := e.svc.PurgeSessions(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n)
	if n != 0 {
		t.Errorf("%d dead sessions left after the purge", n)
	}
}

func TestAcceptInvite(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	admin := access.Actor{UserID: e.admin.ID, Origin: access.OriginWeb}
	if _, err := e.access.CreateProduct(ctx, admin, access.NewProduct{Code: "shop", Name: "Shop"}); err != nil {
		t.Fatal(err)
	}
	invs, err := e.access.CreateInvites(ctx, admin, []string{"new.colleague@example.com"}, []string{"shop"})
	if err != nil {
		t.Fatal(err)
	}
	token := invs[0].Token
	account := access.NewAccount{Username: "ncolleague", DisplayName: "New Colleague"}
	// A refused password leaves the link usable.
	if _, err := e.svc.AcceptInvite(ctx, token, account, "too short"); fieldOf(err) != "password:too_short" {
		t.Fatalf("short password: %v", err)
	}
	if _, err := e.svc.AcceptInvite(ctx, token, access.NewAccount{Username: "Not Valid", DisplayName: "x"}, "<placeholder: long enough>"); err == nil {
		t.Fatal("an invalid account name was accepted")
	}
	in, err := e.svc.AcceptInvite(ctx, token, account, "<placeholder: long enough>")
	if err != nil {
		t.Fatal(err)
	}
	if in.User.Username != "ncolleague" || in.User.Status != access.StatusActive || len(in.User.Memberships) != 1 || in.User.Memberships[0].Product.Code != "shop" {
		t.Errorf("account: %+v", in.User)
	}
	if c, err := e.svc.Session(ctx, in.Token); err != nil || c.Actor.UserID != in.User.ID {
		t.Errorf("signed in: %+v %v", c, err)
	}
	if _, err := e.svc.AcceptInvite(ctx, token, access.NewAccount{Username: "again", DisplayName: "Again"}, "<placeholder: long enough>"); !errors.Is(err, access.ErrInviteExpired) {
		t.Errorf("the link worked twice: %v", err)
	}
	e.login(t, "ncolleague", "<placeholder: long enough>")
	infos, err := e.svc.SignIns(ctx, []string{in.User.ID, e.admin.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := infos[in.User.ID]; len(got.Methods) != 1 || got.Methods[0] != "local" || got.LastLoginAt == nil {
		t.Errorf("sign-ins of the new account: %+v", got)
	}
	if got := infos[e.admin.ID]; got.LastLoginAt != nil {
		t.Errorf("the administrator never signed in: %+v", got)
	}
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	here := e.login(t, "admin", adminPassword)
	there := e.login(t, "admin", adminPassword)
	c, _ := e.svc.Session(ctx, here.Token)
	const next = "<placeholder: the new password>"

	if err := e.svc.ChangePassword(ctx, c, "wrong", next); fieldOf(err) != "current_password:mismatch" {
		t.Fatalf("wrong current password: %v", err)
	}
	if err := e.svc.ChangePassword(ctx, c, adminPassword, "short"); fieldOf(err) != "new_password:too_short" {
		t.Fatalf("short new password: %v", err)
	}
	cli := c
	cli.Actor.Origin, cli.SessionID = access.OriginCLI, ""
	if err := e.svc.ChangePassword(ctx, cli, adminPassword, next); !errors.Is(err, authn.ErrDenied) {
		t.Fatalf("from the command line: %v", err)
	}
	if err := e.svc.ChangePassword(ctx, c, adminPassword, next); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Session(ctx, here.Token); err != nil {
		t.Errorf("this session ended: %v", err)
	}
	if _, err := e.svc.Session(ctx, there.Token); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Errorf("the other session goes on: %v", err)
	}
	if _, err := e.svc.Login(ctx, "admin", adminPassword); !errors.Is(err, authn.ErrInvalidCredentials) {
		t.Errorf("the old password works: %v", err)
	}
	e.login(t, "admin", next)

	// Wrong current passwords lock like wrong sign-ins.
	for range 4 {
		e.svc.ChangePassword(ctx, c, "wrong", adminPassword)
	}
	if _, err := e.svc.Login(ctx, "admin", next); kindOf(err) != authn.KindLocked {
		t.Errorf("after four wrong current passwords: %v", err)
	}
}

func TestSignOutEverywhere(t *testing.T) {
	devs := &devices{byToken: map[string][2]string{}, out: map[string]bool{}}
	e := newEnv(t, devs)
	ctx := context.Background()
	devToken := authn.NewToken(authn.DeviceTokenPrefix)
	devs.byToken[devToken] = [2]string{e.admin.ID, "0193a4b2-6c1e-7a00-8000-000000000201"}
	a, b := e.login(t, "admin", adminPassword), e.login(t, "admin", adminPassword)
	dc, err := e.svc.Device(ctx, devToken)
	if err != nil || dc.Actor.DeviceID == "" || dc.Actor.Origin != access.OriginCLI {
		t.Fatalf("device: %+v %v", dc, err)
	}
	c, _ := e.svc.Session(ctx, a.Token)
	if err := e.svc.SignOutEverywhere(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{a.Token, b.Token} {
		if _, err := e.svc.Session(ctx, tok); !errors.Is(err, authn.ErrUnauthenticated) {
			t.Errorf("a session survived: %v", err)
		}
	}
	if _, err := e.svc.Device(ctx, devToken); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Errorf("the computer survived: %v", err)
	}
}

// Until the device code flow exists, device tokens are refused rather than
// trusted.
func TestDeviceTokensWithoutVerifier(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := e.svc.Device(context.Background(), authn.NewToken(authn.DeviceTokenPrefix)); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Errorf("device token: %v", err)
	}
}

func TestTokenFormat(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		tok := authn.NewToken(authn.AccessTokenPrefix)
		if seen[tok] || !authn.TokenWellFormed(authn.AccessTokenPrefix, tok) || authn.TokenWellFormed(authn.DeviceTokenPrefix, tok) {
			t.Fatalf("token %q", tok)
		}
		seen[tok] = true
		// One changed character fails the check digits.
		b := []byte(tok)
		i := len(authn.AccessTokenPrefix) + 5
		if b[i] == 'a' {
			b[i] = 'b'
		} else {
			b[i] = 'a'
		}
		if authn.TokenWellFormed(authn.AccessTokenPrefix, string(b)) {
			t.Errorf("a changed token passes: %s", b)
		}
		// The log masks it.
		if got := logging.Redact("token " + tok + " used"); strings.Contains(got, tok[8:]) {
			t.Errorf("the log keeps the token: %s", got)
		}
	}
	dev := authn.NewToken(authn.DeviceTokenPrefix)
	if !authn.TokenWellFormed(authn.DeviceTokenPrefix, dev) || authn.TokenWellFormed(authn.AccessTokenPrefix, dev) {
		t.Errorf("device token %q", dev)
	}
}
