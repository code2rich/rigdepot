// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
)

func fieldsOf(err error) []string {
	var e *access.Error
	if !errors.As(err, &e) {
		return nil
	}
	var out []string
	for _, f := range e.Fields {
		out = append(out, f.Field+":"+f.Code)
	}
	return out
}

func TestCreateProduct(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		e := newEnv(t, db)
		ctx := context.Background()
		admin := e.user(t, "admin", access.StatusActive, true)
		owner := e.user(t, "gyuan", access.StatusActive, false)
		pending := e.user(t, "wyan", access.StatusPending, false)

		p, err := e.svc.CreateProduct(ctx, as(admin), access.NewProduct{Code: "shop", Name: "Shop", Line: "Commerce", Owners: []string{owner.ID, pending.ID, owner.ID}})
		must(t, "create", err)
		if p.Code != "shop" || p.Name != "Shop" || p.Line != "Commerce" || p.MemberCount != 2 || len(p.Owners) != 2 || p.MyRole != "" {
			t.Errorf("created: %+v", p)
		}
		// Owning a product activates an account that was waiting.
		u, err := e.svc.GetUser(ctx, as(admin), pending.ID)
		must(t, "get", err)
		if u.Status != access.StatusActive {
			t.Errorf("pending owner is %s, want active", u.Status)
		}

		for _, c := range []struct {
			n    access.NewProduct
			want error
		}{
			{access.NewProduct{Code: "shop", Name: "Again"}, access.ErrCodeTaken},
			{access.NewProduct{Code: "sync", Name: "A command"}, access.ErrCodeReserved},
			{access.NewProduct{Code: "org", Name: "A scope"}, access.ErrCodeReserved},
			{access.NewProduct{Code: "me", Name: "A scope"}, access.ErrCodeReserved},
			{access.NewProduct{Code: "Shop", Name: "Upper"}, access.ErrInvalid},
			{access.NewProduct{Code: "s", Name: "Short"}, access.ErrInvalid},
			{access.NewProduct{Code: "a--b", Name: "Hyphens"}, access.ErrInvalid},
			{access.NewProduct{Code: "fine", Name: ""}, access.ErrInvalid},
			{access.NewProduct{Code: "fine", Name: strings.Repeat("长", 65)}, access.ErrInvalid},
			{access.NewProduct{Code: "fine", Name: "Fine", Owners: []string{"nobody"}}, access.ErrInvalid},
			{access.NewProduct{Code: "fine", Name: "Fine", Owners: []string{"0193a4b2-6c1e-7a00-8000-0000000000ff"}}, access.ErrInvalid},
		} {
			_, err := e.svc.CreateProduct(ctx, as(admin), c.n)
			if !errors.Is(err, c.want) {
				t.Errorf("create %+v: %v, want %v", c.n, err, c.want)
			}
		}
		_, err = e.svc.CreateProduct(ctx, as(admin), access.NewProduct{Code: "s", Name: ""})
		if got := fieldsOf(err); !slices.Equal(got, []string{"code:too_short", "name:required"}) {
			t.Errorf("fields: %v", got)
		}

		// Only administrators create products.
		_, err = e.svc.CreateProduct(ctx, as(owner), access.NewProduct{Code: "pay", Name: "Payments"})
		wantErr(t, "an owner creating a product", err, access.ErrDenied)

		// A product may start without owners.
		empty, err := e.svc.CreateProduct(ctx, as(admin), access.NewProduct{Code: "pay", Name: "Payments"})
		must(t, "create without owners", err)
		if len(empty.Owners) != 0 || empty.MemberCount != 0 {
			t.Errorf("empty product: %+v", empty)
		}

		var created []string
		for _, ev := range e.events.all() {
			if pc, ok := ev.(access.ProductCreated); ok {
				created = append(created, pc.Code)
			}
		}
		if !slices.Equal(created, []string{"shop", "pay"}) {
			t.Errorf("ProductCreated events: %v", created)
		}
	})
}

func TestListAndUpdateProducts(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		e := newEnv(t, db)
		ctx := context.Background()
		admin := e.user(t, "admin", access.StatusActive, true)
		owner := e.user(t, "owner", access.StatusActive, false)
		member := e.user(t, "member", access.StatusActive, false)
		pending := e.user(t, "pending", access.StatusPending, false)
		e.product(t, "shop", owner.ID)
		e.member(t, "shop", member.ID, access.RoleMember)
		e.product(t, "admin-tools")

		// Everyone signed in lists every product, pending accounts too.
		list, err := e.svc.ListProducts(ctx, as(pending))
		must(t, "list", err)
		var codes []string
		for _, p := range list {
			codes = append(codes, p.Code)
		}
		if !slices.Equal(codes, []string{"admin-tools", "shop"}) {
			t.Errorf("products by name: %v", codes)
		}
		p, err := e.svc.GetProduct(ctx, as(member), "shop")
		must(t, "get", err)
		if p.MyRole != access.RoleMember || p.MemberCount != 2 || len(p.Owners) != 1 || p.Owners[0].Username != "owner" {
			t.Errorf("shop as its member: %+v", p)
		}
		_, err = e.svc.GetProduct(ctx, as(member), "nope")
		wantErr(t, "unknown product", err, access.ErrNotFound)

		name, line := "Shop Online", "Retail"
		_, err = e.svc.UpdateProduct(ctx, as(owner), "shop", access.ProductChange{Name: &name})
		wantErr(t, "an owner renaming", err, access.ErrDenied)
		p, err = e.svc.UpdateProduct(ctx, as(admin), "shop", access.ProductChange{Name: &name, Line: &line})
		must(t, "rename", err)
		if p.Code != "shop" || p.Name != name || p.Line != line {
			t.Errorf("renamed: %+v", p)
		}
		if got := e.audit.last(); got.Action != access.ActionProductUpdate || got.Change["name"] == nil {
			t.Errorf("audit: %+v", got)
		}
	})
}

func TestMembers(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		e := newEnv(t, db)
		ctx := context.Background()
		owner := e.user(t, "owner", access.StatusActive, false)
		ann := e.user(t, "ann", access.StatusActive, false)
		bob := e.user(t, "bob", access.StatusPending, false)
		outsider := e.user(t, "outsider", access.StatusActive, false)
		gone := e.user(t, "gone", access.StatusActive, false)
		e.disable(t, gone.ID)
		p := e.product(t, "shop", owner.ID)

		m, added, err := e.svc.PutMember(ctx, as(owner), "shop", bob.ID, access.RoleMember)
		must(t, "add bob", err)
		if !added || m.Role != access.RoleMember || m.User.Username != "bob" {
			t.Errorf("added: %v %+v", added, m)
		}
		bobNow, err := e.svc.GetUser(ctx, as(owner), bob.ID)
		must(t, "get bob", err)
		if bobNow.Status != access.StatusActive {
			t.Errorf("adding a pending account left it %s", bobNow.Status)
		}
		_, added, err = e.svc.PutMember(ctx, as(owner), "shop", bob.ID, access.RoleMember)
		must(t, "add bob again", err)
		if added {
			t.Error("adding an existing member reported as added")
		}
		_, _, err = e.svc.PutMember(ctx, as(owner), "shop", ann.ID, access.RoleOwner)
		must(t, "add ann as owner", err)
		_, _, err = e.svc.PutMember(ctx, as(owner), "shop", gone.ID, access.RoleMember)
		wantErr(t, "adding a disabled account", err, access.ErrUserDisabled)
		_, _, err = e.svc.PutMember(ctx, as(owner), "shop", "0193a4b2-6c1e-7a00-8000-0000000000ff", access.RoleMember)
		wantErr(t, "adding nobody", err, access.ErrNotFound)
		_, _, err = e.svc.PutMember(ctx, as(owner), "shop", ann.ID, access.Role("boss"))
		wantErr(t, "an unknown role", err, access.ErrInvalid)
		_, _, err = e.svc.PutMember(ctx, as(bob), "shop", outsider.ID, access.RoleMember)
		wantErr(t, "a member adding someone", err, access.ErrDenied)

		ms, err := e.svc.ListMembers(ctx, as(bob), "shop")
		must(t, "list", err)
		var order []string
		for _, m := range ms {
			order = append(order, m.User.Username+":"+string(m.Role))
		}
		if !slices.Equal(order, []string{"ann:owner", "owner:owner", "bob:member"}) {
			t.Errorf("members, owners first: %v", order)
		}
		_, err = e.svc.ListMembers(ctx, as(outsider), "shop")
		wantErr(t, "an outsider listing members", err, access.ErrDenied)

		must(t, "remove bob", e.svc.RemoveMember(ctx, as(ann), "shop", bob.ID))
		wantErr(t, "remove bob again", e.svc.RemoveMember(ctx, as(ann), "shop", bob.ID), access.ErrNotFound)

		var changes []string
		for _, ev := range e.events.all() {
			if mc, ok := ev.(access.MembershipChanged); ok && mc.ProductID == p.ID {
				changes = append(changes, string(mc.Old)+">"+string(mc.New))
			}
		}
		if !slices.Equal(changes, []string{">owner", ">member", ">owner", "member>"}) {
			t.Errorf("MembershipChanged events: %v", changes)
		}
	})
}

func TestUsers(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		e := newEnv(t, db)
		ctx := context.Background()
		admin := e.user(t, "admin", access.StatusActive, true)
		owner := e.user(t, "owner", access.StatusActive, false)
		jdoe := e.user(t, "jdoe", access.StatusActive, false)
		e.user(t, "wyan", access.StatusPending, false)
		e.product(t, "shop", owner.ID)
		e.member(t, "shop", jdoe.ID, access.RoleMember)

		all, err := e.svc.ListUsers(ctx, as(owner), access.UserFilter{})
		must(t, "an owner lists", err)
		if len(all) != 4 {
			t.Errorf("%d users, want 4", len(all))
		}
		found, err := e.svc.ListUsers(ctx, as(admin), access.UserFilter{Query: "JDOE@EXAMPLE"})
		must(t, "search", err)
		if len(found) != 1 || found[0].ID != jdoe.ID || len(found[0].Memberships) != 1 || found[0].Memberships[0].Product.Code != "shop" {
			t.Errorf("search by address: %+v", found)
		}
		// % and _ are matched as themselves.
		none, err := e.svc.ListUsers(ctx, as(admin), access.UserFilter{Query: "%"})
		must(t, "search %", err)
		if len(none) != 0 {
			t.Errorf("%% matched %d users", len(none))
		}
		pend, err := e.svc.ListUsers(ctx, as(admin), access.UserFilter{Status: access.StatusPending})
		must(t, "by status", err)
		if len(pend) != 1 || pend[0].Username != "wyan" {
			t.Errorf("pending: %+v", pend)
		}
		_, err = e.svc.ListUsers(ctx, as(jdoe), access.UserFilter{})
		wantErr(t, "a member listing users", err, access.ErrDenied)
		_, err = e.svc.GetUser(ctx, as(jdoe), owner.ID)
		wantErr(t, "a member reading someone else", err, access.ErrDenied)
		me, err := e.svc.Me(ctx, as(jdoe))
		must(t, "me", err)
		if me.Username != "jdoe" || len(me.Memberships) != 1 || me.Admin {
			t.Errorf("me: %+v", me)
		}
	})
}

func TestUpdateProfile(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		e := newEnv(t, db)
		ctx := context.Background()
		pending := e.user(t, "wyan", access.StatusPending, false)
		job, locale := "Developer", "zh-CN"
		stages := []string{"design", "develop", "design"}
		u, err := e.svc.UpdateProfile(ctx, as(pending), access.ProfileChange{Job: &job, Locale: &locale, FocusStages: &stages})
		must(t, "update", err)
		if u.Job != job || u.Locale != locale || !slices.Equal(u.FocusStages, []string{"design", "develop"}) {
			t.Errorf("updated: %+v", u)
		}
		again, err := e.svc.Me(ctx, as(pending))
		must(t, "me", err)
		if !slices.Equal(again.FocusStages, u.FocusStages) || again.Job != job {
			t.Errorf("read back: %+v", again)
		}
		bad := []string{"Design"}
		empty := ""
		_, err = e.svc.UpdateProfile(ctx, as(pending), access.ProfileChange{FocusStages: &bad, DisplayName: &empty})
		if got := fieldsOf(err); !slices.Equal(got, []string{"display_name:required", "focus_stages[0]:pattern"}) {
			t.Errorf("fields: %v (%v)", got, err)
		}
		before := len(e.audit.all())
		_, err = e.svc.UpdateProfile(ctx, as(pending), access.ProfileChange{Job: &job})
		must(t, "no change", err)
		if len(e.audit.all()) != before {
			t.Error("a change that changes nothing was audited")
		}
	})
}

func TestJoinRequests(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		e := newEnv(t, db)
		ctx := context.Background()
		admin := e.user(t, "admin", access.StatusActive, true)
		shopOwner := e.user(t, "gyuan", access.StatusActive, false)
		payOwner := e.user(t, "blu", access.StatusActive, false)
		newbie := e.user(t, "wyan", access.StatusPending, false)
		e.product(t, "shop", shopOwner.ID)
		e.product(t, "pay", payOwner.ID)

		rs, err := e.svc.RequestToJoin(ctx, as(newbie), []string{"shop", "pay", "shop"}, "Joining the shop backend team")
		must(t, "ask", err)
		if len(rs) != 2 || rs[0].Status != access.JoinPending || rs[0].Product.Code != "shop" || rs[1].Product.Code != "pay" {
			t.Fatalf("requests: %+v", rs)
		}
		again, err := e.svc.RequestToJoin(ctx, as(newbie), []string{"shop"}, "")
		must(t, "ask again", err)
		if len(again) != 1 || again[0].ID != rs[0].ID {
			t.Errorf("asking again made a second request: %+v", again)
		}
		_, err = e.svc.RequestToJoin(ctx, as(newbie), []string{"nope"}, "")
		wantErr(t, "an unknown product", err, access.ErrNotFound)
		_, err = e.svc.RequestToJoin(ctx, as(shopOwner), []string{"shop"}, "")
		wantErr(t, "asking to join one's own product", err, access.ErrAlreadyMember)

		mine, err := e.svc.MyJoinRequests(ctx, as(newbie))
		must(t, "mine", err)
		if len(mine) != 2 {
			t.Errorf("my requests: %d", len(mine))
		}
		// Owners see the requests for their products only; admins all.
		forShop, err := e.svc.JoinRequests(ctx, as(shopOwner), access.JoinFilter{})
		must(t, "owner list", err)
		if len(forShop) != 1 || forShop[0].Product.Code != "shop" {
			t.Errorf("shop owner sees: %+v", forShop)
		}
		_, err = e.svc.JoinRequests(ctx, as(shopOwner), access.JoinFilter{Product: "pay"})
		wantErr(t, "shop owner filtering on pay", err, access.ErrDenied)
		forAdmin, err := e.svc.JoinRequests(ctx, as(admin), access.JoinFilter{})
		must(t, "admin list", err)
		if len(forAdmin) != 2 {
			t.Errorf("admin sees %d", len(forAdmin))
		}

		// The pay owner cannot decide on shop.
		_, err = e.svc.ApproveJoinRequest(ctx, as(payOwner), rs[0].ID)
		wantErr(t, "pay owner approving shop", err, access.ErrDenied)

		approved, err := e.svc.ApproveJoinRequest(ctx, as(shopOwner), rs[0].ID)
		must(t, "approve", err)
		if approved.Status != access.JoinApproved || approved.DecidedBy == nil || approved.DecidedBy.ID != shopOwner.ID || approved.DecidedAt == nil {
			t.Errorf("approved: %+v", approved)
		}
		me, err := e.svc.Me(ctx, as(newbie))
		must(t, "me", err)
		if me.Status != access.StatusActive || len(me.Memberships) != 1 || me.Memberships[0].Role != access.RoleMember {
			t.Errorf("after approval: %+v", me)
		}
		_, err = e.svc.ApproveJoinRequest(ctx, as(shopOwner), rs[0].ID)
		wantErr(t, "approving twice", err, access.ErrDecided)

		_, err = e.svc.RejectJoinRequest(ctx, as(payOwner), rs[1].ID, "")
		wantErr(t, "rejecting without a reason", err, access.ErrInvalid)
		rejected, err := e.svc.RejectJoinRequest(ctx, as(payOwner), rs[1].ID, "Ask your lead first")
		must(t, "reject", err)
		if rejected.Status != access.JoinRejected || rejected.Reason != "Ask your lead first" {
			t.Errorf("rejected: %+v", rejected)
		}
		done, err := e.svc.JoinRequests(ctx, as(admin), access.JoinFilter{Status: access.JoinRejected})
		must(t, "rejected list", err)
		if len(done) != 1 {
			t.Errorf("rejected: %d", len(done))
		}
		_, err = e.svc.ApproveJoinRequest(ctx, as(admin), "0193a4b2-6c1e-7a00-8000-0000000000ff")
		wantErr(t, "approving nothing", err, access.ErrNotFound)

		var decided []bool
		for _, ev := range e.events.all() {
			if d, ok := ev.(access.JoinDecided); ok {
				decided = append(decided, d.Approved)
			}
		}
		if !slices.Equal(decided, []bool{true, false}) {
			t.Errorf("JoinDecided events: %v", decided)
		}
	})
}

func TestOnePendingRequestUnderConcurrency(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		e := newEnv(t, db)
		owner := e.user(t, "owner", access.StatusActive, false)
		newbie := e.user(t, "wyan", access.StatusPending, false)
		e.product(t, "shop", owner.ID)
		errs := race(5, func(int) error {
			_, err := e.svc.RequestToJoin(context.Background(), as(newbie), []string{"shop"}, "")
			return err
		})
		for _, err := range errs {
			must(t, "ask", err)
		}
		var n int
		must(t, "count", e.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM join_requests`).Scan(&n))
		if n != 1 {
			t.Errorf("%d requests, want 1", n)
		}
	})
}

func TestInvites(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		e := newEnv(t, db)
		now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
		e.svc = access.New(db, access.Options{Auditor: e.audit, Events: e.events, Now: func() time.Time { return now }})
		ctx := context.Background()
		admin := e.user(t, "admin", access.StatusActive, true)
		owner := e.user(t, "owner", access.StatusActive, false)
		e.product(t, "shop", owner.ID)

		_, err := e.svc.CreateInvites(ctx, as(owner), []string{"new@example.com"}, nil)
		wantErr(t, "an owner inviting", err, access.ErrDenied)
		_, err = e.svc.CreateInvites(ctx, as(admin), []string{"owner@example.com"}, nil)
		wantErr(t, "inviting an existing account", err, access.ErrAccountExists)
		_, err = e.svc.CreateInvites(ctx, as(admin), []string{"not an address", "Someone <a@example.com>"}, nil)
		if got := fieldsOf(err); !slices.Equal(got, []string{"emails[0]:format", "emails[1]:format"}) {
			t.Errorf("fields: %v", got)
		}

		first, err := e.svc.CreateInvites(ctx, as(admin), []string{"New.Colleague@example.com"}, []string{"shop"})
		must(t, "invite", err)
		if len(first) != 1 || first[0].Email != "new.colleague@example.com" || len(first[0].Products) != 1 ||
			first[0].Token == "" || !first[0].ExpiresAt.Equal(now.Add(access.InviteValidity)) || first[0].CreatedBy.ID != admin.ID {
			t.Fatalf("invite: %+v", first)
		}
		var digest string
		must(t, "read digest", e.db.QueryRowContext(ctx, `SELECT token_digest FROM invites WHERE id = ?`, first[0].ID).Scan(&digest))
		if strings.Contains(digest, first[0].Token) {
			t.Error("the token is stored as it is")
		}
		// Inviting the same address again withdraws the first link.
		second, err := e.svc.CreateInvites(ctx, as(admin), []string{"new.colleague@example.com"}, []string{"shop"})
		must(t, "invite again", err)
		_, err = e.svc.PreviewInvite(ctx, first[0].Token)
		wantErr(t, "the first link after a second invitation", err, access.ErrInviteExpired)
		open, err := e.svc.ListInvites(ctx, as(admin))
		must(t, "list", err)
		if len(open) != 1 || open[0].ID != second[0].ID || open[0].Token != "" {
			t.Errorf("open invitations: %+v", open)
		}

		pv, err := e.svc.PreviewInvite(ctx, second[0].Token)
		must(t, "preview", err)
		if pv.Email != "new.colleague@example.com" || len(pv.Products) != 1 || pv.Products[0].Code != "shop" {
			t.Errorf("preview: %+v", pv)
		}
		_, err = e.svc.PreviewInvite(ctx, "no-such-token-at-all")
		wantErr(t, "an unknown token", err, access.ErrNotFound)

		var u access.User
		must(t, "accept", e.db.InTx(ctx, func(tx *dbx.Tx) error {
			var err error
			u, err = e.svc.AcceptInvite(ctx, tx, second[0].Token, access.NewAccount{Username: "ncolleague", DisplayName: "New Colleague"})
			return err
		}))
		if u.Status != access.StatusActive || u.Email != "new.colleague@example.com" || u.EmailVerified ||
			len(u.Memberships) != 1 || u.Memberships[0].Product.Code != "shop" {
			t.Errorf("accepted: %+v", u)
		}
		err = e.db.InTx(ctx, func(tx *dbx.Tx) error {
			_, err := e.svc.AcceptInvite(ctx, tx, second[0].Token, access.NewAccount{Username: "twice", DisplayName: "Twice"})
			return err
		})
		wantErr(t, "accepting twice", err, access.ErrInviteExpired)

		// An expired link.
		third, err := e.svc.CreateInvites(ctx, as(admin), []string{"late@example.com"}, nil)
		must(t, "invite late", err)
		now = now.Add(access.InviteValidity)
		_, err = e.svc.PreviewInvite(ctx, third[0].Token)
		wantErr(t, "an expired link", err, access.ErrInviteExpired)

		// Revoking.
		now = now.Add(-time.Hour)
		must(t, "revoke", e.svc.RevokeInvite(ctx, as(admin), third[0].ID))
		_, err = e.svc.PreviewInvite(ctx, third[0].Token)
		wantErr(t, "a withdrawn link", err, access.ErrInviteExpired)
		wantErr(t, "revoke unknown", e.svc.RevokeInvite(ctx, as(admin), "0193a4b2-6c1e-7a00-8000-0000000000ff"), access.ErrNotFound)

		for _, ev := range e.events.all() {
			if ic, ok := ev.(access.InviteCreated); ok && ic.Email == "" {
				t.Errorf("InviteCreated without an address: %+v", ic)
			}
		}
		for _, entry := range e.audit.all() {
			for _, inv := range []access.Invite{first[0], second[0], third[0]} {
				for _, v := range entry.Change {
					if s, ok := v.(string); ok && strings.Contains(s, inv.Token) {
						t.Errorf("an invitation token is in the audit log: %+v", entry)
					}
				}
			}
		}
	})
}

func TestUsernameTakenWhenAccepting(t *testing.T) {
	e := sqlite(t)
	ctx := context.Background()
	admin := e.user(t, "admin", access.StatusActive, true)
	inv, err := e.svc.CreateInvites(ctx, as(admin), []string{"x@example.com"}, nil)
	must(t, "invite", err)
	err = e.db.InTx(ctx, func(tx *dbx.Tx) error {
		_, err := e.svc.AcceptInvite(ctx, tx, inv[0].Token, access.NewAccount{Username: "Admin", DisplayName: "Copycat"})
		return err
	})
	wantErr(t, "a taken username", err, access.ErrUsernameTaken)
}
