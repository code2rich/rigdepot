// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
)

func countAdmins(t *testing.T, e *env) int {
	t.Helper()
	var n int
	if err := e.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM users WHERE is_admin = TRUE AND status = 'active'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countOwners(t *testing.T, e *env, productID string) int {
	t.Helper()
	var n int
	if err := e.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM memberships WHERE product_id = ? AND role = 'owner'`, productID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTheLastAdministratorStays(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		e := newEnv(t, db)
		ctx := context.Background()
		a := e.user(t, "first", access.StatusActive, true)
		no, disabled := false, access.StatusDisabled

		_, err := e.svc.UpdateUser(ctx, as(a), a.ID, access.UserChange{Admin: &no})
		wantErr(t, "taking one's own rights as the only administrator", err, access.ErrLastAdmin)
		_, err = e.svc.UpdateUser(ctx, as(a), a.ID, access.UserChange{Status: &disabled})
		wantErr(t, "disabling the only administrator", err, access.ErrLastAdmin)
		_, err = e.svc.UpdateUser(ctx, access.System, a.ID, access.UserChange{Status: &disabled, Admin: &no})
		wantErr(t, "the system disabling the only administrator", err, access.ErrLastAdmin)
		if n := countAdmins(t, e); n != 1 {
			t.Fatalf("%d administrators, want 1", n)
		}

		// With a second one, either may go.
		b := e.user(t, "second", access.StatusActive, false)
		yes := true
		_, err = e.svc.UpdateUser(ctx, as(a), b.ID, access.UserChange{Admin: &yes})
		must(t, "grant", err)
		got, err := e.svc.UpdateUser(ctx, as(b), a.ID, access.UserChange{Status: &disabled})
		must(t, "disable the first", err)
		if got.Status != access.StatusDisabled || !got.Admin {
			t.Errorf("after disabling: %+v", got)
		}
		// A disabled administrator does not count: b is the last one now.
		_, err = e.svc.UpdateUser(ctx, as(b), b.ID, access.UserChange{Admin: &no})
		wantErr(t, "taking the rights of the last active administrator", err, access.ErrLastAdmin)
		// Restoring the first one makes two again.
		active := access.StatusActive
		_, err = e.svc.UpdateUser(ctx, as(b), a.ID, access.UserChange{Status: &active})
		must(t, "restore", err)
		if n := countAdmins(t, e); n != 2 {
			t.Errorf("%d administrators, want 2", n)
		}
	})
}

// TestTheLastAdministratorStaysUnderConcurrency takes the rights of every
// administrator at the same moment, each in its own request. Exactly one has
// to fail: the check and the change are in one transaction under one lock.
func TestTheLastAdministratorStaysUnderConcurrency(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		e := newEnv(t, db)
		const n = 6
		var admins []access.User
		for i := range n {
			admins = append(admins, e.user(t, fmt.Sprintf("admin%d", i), access.StatusActive, true))
		}
		errs := race(n, func(i int) error {
			no := false
			disabled := access.StatusDisabled
			c := access.UserChange{Admin: &no}
			if i%2 == 1 {
				c = access.UserChange{Status: &disabled}
			}
			_, err := e.svc.UpdateUser(context.Background(), access.System, admins[i].ID, c)
			return err
		})
		ok, last := tally(t, errs, access.ErrLastAdmin)
		if ok != n-1 || last != 1 {
			t.Errorf("%d succeeded and %d were refused as the last administrator, want %d and 1", ok, last, n-1)
		}
		if got := countAdmins(t, e); got != 1 {
			t.Errorf("%d active administrators left, want 1", got)
		}
	})
}

func TestTheLastOwnerStays(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		e := newEnv(t, db)
		ctx := context.Background()
		admin := e.user(t, "admin", access.StatusActive, true)
		o1 := e.user(t, "o1", access.StatusActive, false)
		p := e.product(t, "shop", o1.ID)

		_, _, err := e.svc.PutMember(ctx, as(o1), "shop", o1.ID, access.RoleMember)
		wantErr(t, "the only owner stepping down", err, access.ErrLastOwner)
		err = e.svc.RemoveMember(ctx, as(admin), "shop", o1.ID)
		wantErr(t, "removing the only owner", err, access.ErrLastOwner)
		var le *access.Error
		if errors.As(err, &le) && le.Params["product"] != "shop" {
			t.Errorf("params: %v", le.Params)
		}

		o2 := e.user(t, "o2", access.StatusActive, false)
		e.member(t, "shop", o2.ID, access.RoleOwner)
		_, _, err = e.svc.PutMember(ctx, as(o1), "shop", o1.ID, access.RoleMember)
		must(t, "stepping down with another owner", err)
		err = e.svc.RemoveMember(ctx, as(o2), "shop", o2.ID)
		wantErr(t, "the new only owner leaving", err, access.ErrLastOwner)
		// Members come and go freely.
		must(t, "remove a member", e.svc.RemoveMember(ctx, as(o2), "shop", o1.ID))
		if n := countOwners(t, e, p.ID); n != 1 {
			t.Errorf("%d owners, want 1", n)
		}
		// Disabling an owner's account does not touch the product.
		e.disable(t, o2.ID)
		if n := countOwners(t, e, p.ID); n != 1 {
			t.Errorf("%d owners after disabling the owner, want 1", n)
		}
	})
}

func TestTheLastOwnerStaysUnderConcurrency(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		e := newEnv(t, db)
		const n = 6
		var owners []string
		for i := range n {
			owners = append(owners, e.user(t, fmt.Sprintf("owner%d", i), access.StatusActive, false).ID)
		}
		p := e.product(t, "shop", owners...)
		errs := race(n, func(i int) error {
			// Half step down, half are removed; each one is acting on
			// themselves, as an owner may.
			me := access.Actor{UserID: owners[i], Origin: access.OriginWeb}
			if i%2 == 0 {
				_, _, err := e.svc.PutMember(context.Background(), me, "shop", owners[i], access.RoleMember)
				return err
			}
			return e.svc.RemoveMember(context.Background(), me, "shop", owners[i])
		})
		// Someone who stepped down is no longer an owner and may be refused
		// as such; but never may the product end up without one.
		if got := countOwners(t, e, p.ID); got != 1 {
			t.Errorf("%d owners left, want 1 (errors %v)", got, errs)
		}
		for _, err := range errs {
			if err != nil && !errors.Is(err, access.ErrLastOwner) && !errors.Is(err, access.ErrDenied) {
				t.Errorf("unexpected error: %v", err)
			}
		}
	})
}

func TestTheLastOwnerStaysUnderConcurrencyBySystem(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		e := newEnv(t, db)
		const n = 6
		var owners []string
		for i := range n {
			owners = append(owners, e.user(t, fmt.Sprintf("owner%d", i), access.StatusActive, false).ID)
		}
		p := e.product(t, "shop", owners...)
		errs := race(n, func(i int) error {
			if i%2 == 0 {
				_, _, err := e.svc.PutMember(context.Background(), access.System, "shop", owners[i], access.RoleMember)
				return err
			}
			return e.svc.RemoveMember(context.Background(), access.System, "shop", owners[i])
		})
		ok, last := tally(t, errs, access.ErrLastOwner)
		if ok != n-1 || last != 1 {
			t.Errorf("%d succeeded and %d were refused as the last owner, want %d and 1", ok, last, n-1)
		}
		if got := countOwners(t, e, p.ID); got != 1 {
			t.Errorf("%d owners left, want 1", got)
		}
	})
}

// race runs fn(0) … fn(n-1) at the same moment and returns their errors.
func race(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	var start, done sync.WaitGroup
	start.Add(1)
	for i := range n {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			errs[i] = fn(i)
		}()
	}
	start.Done()
	done.Wait()
	return errs
}

// tally counts the nil errors and the errors that are want; any other error
// fails the test.
func tally(t *testing.T, errs []error, want error) (ok, matched int) {
	t.Helper()
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, want):
			matched++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	return ok, matched
}

func TestAWriteFailsWhenItsAuditEntryCannotBeWritten(t *testing.T) {
	e := sqlite(t)
	ctx := context.Background()
	admin := e.user(t, "admin", access.StatusActive, true)
	e.audit.fail = errors.New("audit log is full")
	_, err := e.svc.CreateProduct(ctx, as(admin), access.NewProduct{Code: "shop", Name: "Shop"})
	if err == nil || !errors.Is(err, e.audit.fail) {
		t.Fatalf("create with a failing audit log: %v", err)
	}
	e.audit.fail = nil
	if _, err := e.svc.GetProduct(ctx, as(admin), "shop"); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("the product exists although its audit entry failed: %v", err)
	}
}

func TestDeniedWritesAreAudited(t *testing.T) {
	e := sqlite(t)
	ctx := context.Background()
	member := e.user(t, "member", access.StatusActive, false)
	_, err := e.svc.CreateProduct(ctx, as(member), access.NewProduct{Code: "shop", Name: "Shop"})
	wantErr(t, "a member creating a product", err, access.ErrDenied)
	got := e.audit.last()
	if got.Action != access.ActionProductCreate || got.Result != access.AuditDenied || got.Reason != access.ReasonNotAllowed ||
		got.Actor.UserID != member.ID || got.ObjectLabel != "shop" {
		t.Errorf("audit entry of the denial: %+v", got)
	}
	if n := e.committed(t, access.ActionProductCreate); n != 1 {
		t.Errorf("%d committed product.create entries, want the denial", n)
	}
	// Reads are not audited.
	before := len(e.audit.all())
	if _, err := e.svc.ListInvites(ctx, as(member)); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("a member listing invitations: %v", err)
	}
	if len(e.audit.all()) != before {
		t.Error("a denied read was audited")
	}
}
