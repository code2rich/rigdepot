// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access_test

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
)

// matrix is the permission model written out by hand, independently of the
// rule table in action.go: for every action, what each role may do in each
// scope. It is the acceptance test "three roles × three scopes × every
// action" (docs/architecture/12-tasks.md M1-12). Change it only together with
// docs/subsystems/access-roles.md.
//
// Each row has five groups, one per role:
//
//	admin     a platform administrator who is in no product
//	owner     an owner of product P
//	member    a member of product P
//	outsider  an activated account in no product
//	pending   an account waiting to be activated
//
// and each group five letters, one per target, y for allowed:
//
//	org    something for the whole organisation
//	P      product P, or something in its scope
//	Q      another product, which the owner and the member are not in
//	me     the actor's own (personal resource, own account)
//	other  someone else's own
var matrix = map[access.Action]string{
	//                               admin owner member outsider pending
	//                               oPQmo oPQmo oPQmo  oPQmo    oPQmo
	access.ActionProductRead:       "yyyyy yyyyy yyyyy yyyyy yyyyy",
	access.ActionProductCreate:     "yyyyy ..... ..... ..... .....",
	access.ActionProductUpdate:     "yyyyy ..... ..... ..... .....",
	access.ActionMemberRead:        "yyyyy .y... .y... ..... .....",
	access.ActionMemberAdd:         "yyyyy .y... ..... ..... .....",
	access.ActionMemberUpdate:      "yyyyy .y... ..... ..... .....",
	access.ActionMemberRemove:      "yyyyy .y... ..... ..... .....",
	access.ActionJoinCreate:        "yyyyy yyyyy yyyyy yyyyy yyyyy",
	access.ActionJoinRead:          "yyyyy .y... ..... ..... .....",
	access.ActionJoinApprove:       "yyyyy .y... ..... ..... .....",
	access.ActionJoinReject:        "yyyyy .y... ..... ..... .....",
	access.ActionUserRead:          "yyyyy yyyyy ...y. ...y. ...y.",
	access.ActionUserProfile:       "...y. ...y. ...y. ...y. ...y.",
	access.ActionUserDisable:       "yyyyy ..... ..... ..... .....",
	access.ActionUserEnable:        "yyyyy ..... ..... ..... .....",
	access.ActionUserAdminGrant:    "yyyyy ..... ..... ..... .....",
	access.ActionUserAdminDrop:     "yyyyy ..... ..... ..... .....",
	access.ActionUserCreate:        "yyyyy ..... ..... ..... .....",
	access.ActionInviteCreate:      "yyyyy ..... ..... ..... .....",
	access.ActionInviteRead:        "yyyyy ..... ..... ..... .....",
	access.ActionInviteRevoke:      "yyyyy ..... ..... ..... .....",
	access.ActionInviteAccept:      "..... ..... ..... ..... .....",
	access.ActionResourceRead:      "yyyyy yy.y. yy.y. y..y. .....",
	access.ActionResourceDraftRead: "yyyy. .y.y. ...y. ...y. .....",
	access.ActionResourceCreate:    "yyyy. .y.y. ...y. ...y. .....",
	access.ActionResourceEdit:      "yyyy. .y.y. ...y. ...y. .....",
	access.ActionResourcePublish:   "yyyy. .y.y. ...y. ...y. .....",
	access.ActionResourceConfigure: "yyyy. .y... ..... ..... .....",
	access.ActionResourceRollback:  "yyyy. .y.y. ...y. ...y. .....",
	access.ActionResourceDisable:   "yyyyy .y.y. ...y. ...y. .....",
	access.ActionResourceEnable:    "yyyyy .y.y. ...y. ...y. .....",
	access.ActionResourceWaive:     "yyyy. ..... ..... ..... .....",
	access.ActionResourcePrefer:    "y..y. yy.y. yy.y. y..y. .....",
	access.ActionInstallsRead:      "yyyyy .y... ..... ..... .....",
	access.ActionSourceManage:      "yyyyy ..... ..... ..... .....",
	access.ActionSecretDefine:      "yyyyy ..... ..... ..... .....",
	access.ActionFeedbackCreate:    "yyyyy yyyyy yyyyy yyyyy .....",
	access.ActionFeedbackManage:    "yyyy. .y... ..... ..... .....",
	access.ActionAuditRead:         "yyyyy ..... ..... ..... .....",
	access.ActionSettingsManage:    "yyyyy ..... ..... ..... .....",
	access.ActionAdapterManage:     "yyyyy ..... ..... ..... .....",
}

var (
	roleNames   = []string{"admin", "owner", "member", "outsider", "pending"}
	targetNames = []string{"org", "P", "Q", "me", "other"}
)

// world is the cast of the matrix in one database.
type world struct {
	*env
	roles    map[string]access.User
	other    access.User
	disabled access.User
	p, q     access.Product
}

func newWorld(t *testing.T, db *dbx.DB) *world {
	t.Helper()
	e := newEnv(t, db)
	w := &world{env: e, roles: map[string]access.User{}}
	w.roles["admin"] = e.user(t, "admin", access.StatusActive, true)
	w.roles["owner"] = e.user(t, "owner", access.StatusActive, false)
	w.roles["member"] = e.user(t, "member", access.StatusActive, false)
	w.roles["outsider"] = e.user(t, "outsider", access.StatusActive, false)
	w.roles["pending"] = e.user(t, "pending", access.StatusPending, false)
	w.other = e.user(t, "other", access.StatusActive, false)
	w.disabled = e.user(t, "gone", access.StatusActive, false)
	w.p = e.product(t, "shop", w.roles["owner"].ID)
	e.member(t, "shop", w.roles["member"].ID, access.RoleMember)
	e.member(t, "shop", w.disabled.ID, access.RoleOwner)
	w.q = e.product(t, "pay", w.other.ID)
	e.disable(t, w.disabled.ID)
	return w
}

func (w *world) target(name string, actor access.User) access.Target {
	switch name {
	case "org":
		return access.TargetOrg()
	case "P":
		return access.TargetProduct(w.p.ID)
	case "Q":
		return access.TargetProduct(w.q.ID)
	case "me":
		return access.TargetPersonal(actor.ID)
	case "other":
		return access.TargetPersonal(w.other.ID)
	}
	panic(name)
}

func TestPermissionMatrix(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		w := newWorld(t, db)
		ctx := context.Background()
		for _, action := range slices.Sorted(maps.Keys(matrix)) {
			want := matrix[action]
			groups := strings.Fields(want)
			if len(groups) != len(roleNames) {
				t.Fatalf("%s: want %d groups, have %q", action, len(roleNames), want)
			}
			for i, role := range roleNames {
				actor := w.roles[role]
				if len(groups[i]) != len(targetNames) {
					t.Fatalf("%s %s: want %d letters, have %q", action, role, len(targetNames), groups[i])
				}
				for j, target := range targetNames {
					want := groups[i][j] == 'y'
					d := w.svc.Can(ctx, as(actor), action, w.target(target, actor))
					if d.Err != nil {
						t.Fatalf("%s %s %s: %v", action, role, target, d.Err)
					}
					if d.Allowed != want {
						t.Errorf("%-22s %-8s on %-5s: allowed=%v, want %v (reason %q)", action, role, target, d.Allowed, want, d.Reason)
					}
				}
			}
			// A disabled account may do nothing, whatever its roles.
			for _, target := range targetNames {
				if d := w.svc.Can(ctx, as(w.disabled), action, w.target(target, w.disabled)); d.Allowed || d.Reason != access.ReasonDisabled {
					t.Errorf("%s by a disabled owner of P on %s: allowed=%v reason=%q, want denied as disabled", action, target, d.Allowed, d.Reason)
				}
			}
			// The server itself may do everything.
			if d := w.svc.Can(ctx, access.System, action, access.TargetPlatform()); !d.Allowed {
				t.Errorf("%s by the system: denied (%s)", action, d.Reason)
			}
		}
	})
}

// TestMatrixCoversEveryAction keeps the matrix complete: a new action needs
// its row before it can be used. (A row twice does not compile.)
func TestMatrixCoversEveryAction(t *testing.T) {
	listed := slices.Sorted(maps.Keys(matrix))
	for _, a := range access.Actions() {
		if !slices.Contains(listed, a) {
			t.Errorf("action %s has no row in the permission matrix", a)
		}
	}
	for _, a := range listed {
		if !slices.Contains(access.Actions(), a) {
			t.Errorf("the matrix lists %s, which is not an action", a)
		}
	}
}

func TestUnknownActionAndUnknownUserAreDenied(t *testing.T) {
	w := newWorld(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	ctx := context.Background()
	if d := w.svc.Can(ctx, as(w.roles["admin"]), access.Action("product.explode"), access.TargetPlatform()); d.Allowed || d.Reason != access.ReasonUnknown {
		t.Errorf("unknown action: %+v", d)
	}
	ghost := access.Actor{UserID: "0193a4b2-6c1e-7a00-8000-0000000000ff", Origin: access.OriginWeb}
	if d := w.svc.Can(ctx, ghost, access.ActionProductRead, access.TargetPlatform()); d.Allowed || d.Reason != access.ReasonUnknownUser {
		t.Errorf("unknown user: %+v", d)
	}
	if d := w.svc.Can(ctx, access.Actor{Origin: access.OriginWeb}, access.ActionProductRead, access.TargetPlatform()); d.Allowed {
		t.Errorf("no user: %+v", d)
	}
}

func TestMaintainersPublishWithoutBeingOwners(t *testing.T) {
	w := newWorld(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	ctx := context.Background()
	outsider := w.roles["outsider"]
	// A resource in product Q, which the outsider maintains without being in Q.
	res := access.TargetResource(access.InProduct, w.q.ID, "", []string{outsider.ID})
	for action, want := range map[access.Action]bool{
		access.ActionResourceRead:      true,
		access.ActionResourceDraftRead: true,
		access.ActionResourceEdit:      true,
		access.ActionResourcePublish:   true,
		access.ActionResourceWaive:     false, // false positives are the administrators' call
		access.ActionFeedbackManage:    true,
		access.ActionResourceConfigure: false, // scope and policy are the owners'
		access.ActionResourceRollback:  false, // owners roll back what maintainers publish
		access.ActionResourceDisable:   false,
		access.ActionResourcePrefer:    false, // not in Q, so not for them
	} {
		if d := w.svc.Can(ctx, as(outsider), action, res); d.Allowed != want {
			t.Errorf("maintainer %s: allowed=%v, want %v", action, d.Allowed, want)
		}
	}
	// Maintaining does not help a pending account.
	if d := w.svc.Can(ctx, as(w.roles["pending"]), access.ActionResourcePublish,
		access.TargetResource(access.InOrg, "", "", []string{w.roles["pending"].ID})); d.Allowed {
		t.Error("a pending maintainer may publish")
	}
}

func TestAccessTokensAreScopeIntersectedWithTheirOwner(t *testing.T) {
	w := newWorld(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	ctx := context.Background()
	token := func(u access.User, s access.TokenScope) access.Actor {
		return access.Actor{UserID: u.ID, Origin: access.OriginCLI, TokenID: "t", Scope: s}
	}
	inP := access.TargetResource(access.InProduct, w.p.ID, "", nil)
	cases := []struct {
		actor  access.Actor
		action access.Action
		want   bool
		reason string
	}{
		{token(w.roles["owner"], access.TokenRead), access.ActionResourceRead, true, ""},
		{token(w.roles["owner"], access.TokenRead), access.ActionResourcePublish, false, access.ReasonTokenScope},
		{token(w.roles["owner"], access.TokenPublish), access.ActionResourcePublish, true, ""},
		{token(w.roles["owner"], access.TokenPublish), access.ActionResourceRead, true, ""},
		{token(w.roles["owner"], access.TokenPublish), access.ActionMemberAdd, false, access.ReasonTokenScope},
		{token(w.roles["admin"], access.TokenPublish), access.ActionSettingsManage, false, access.ReasonTokenScope},
		{token(w.roles["admin"], access.TokenRead), access.ActionAuditRead, true, ""},
		// The scope never adds to what the person may do.
		{token(w.roles["member"], access.TokenPublish), access.ActionResourcePublish, false, access.ReasonNotAllowed},
		{token(w.roles["outsider"], access.TokenRead), access.ActionResourceRead, false, access.ReasonNotAllowed},
		{access.Actor{UserID: w.roles["owner"].ID, Scope: "admin"}, access.ActionResourceRead, false, access.ReasonTokenScope},
	}
	for _, c := range cases {
		d := w.svc.Can(ctx, c.actor, c.action, inP)
		if d.Allowed != c.want || (!c.want && d.Reason != c.reason) {
			t.Errorf("token %q of %s, %s: %+v, want allowed=%v reason=%q", c.actor.Scope, c.actor.UserID, c.action, d, c.want, c.reason)
		}
	}
}

// TestLeavingAProductTakesEffectAtOnce: nothing about permissions is cached;
// the next decision after someone is taken out of a product no longer lets
// them see its resources, also through a token issued before.
func TestLeavingAProductTakesEffectAtOnce(t *testing.T) {
	w := newWorld(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	ctx := context.Background()
	m := w.roles["member"]
	tok := access.Actor{UserID: m.ID, Origin: access.OriginCLI, TokenID: "t", Scope: access.TokenRead}
	inP := access.TargetResource(access.InProduct, w.p.ID, "", nil)
	if d := w.svc.Can(ctx, tok, access.ActionResourceRead, inP); !d.Allowed {
		t.Fatalf("before: %+v", d)
	}
	v, err := w.svc.VisibilityOf(ctx, as(m))
	must(t, "visibility", err)
	if !v.Includes(inP) || !v.Includes(access.TargetOrg()) || v.Includes(access.TargetProduct(w.q.ID)) {
		t.Fatalf("visibility before: %+v", v)
	}
	must(t, "remove", w.svc.RemoveMember(ctx, as(w.roles["owner"]), "shop", m.ID))
	if d := w.svc.Can(ctx, tok, access.ActionResourceRead, inP); d.Allowed {
		t.Error("after leaving P its resources are still readable")
	}
	v, err = w.svc.VisibilityOf(ctx, as(m))
	must(t, "visibility", err)
	if v.Includes(inP) || !v.Includes(access.TargetOrg()) {
		t.Errorf("visibility after: %+v", v)
	}
	w.disable(t, m.ID)
	v, err = w.svc.VisibilityOf(ctx, as(m))
	must(t, "visibility", err)
	if v.Includes(access.TargetOrg()) || v.Includes(access.TargetPersonal(m.ID)) {
		t.Errorf("a disabled account still sees: %+v", v)
	}
}
