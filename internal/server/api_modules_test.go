// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/module"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// greeter is a module that uses everything the host hands out on each request: who is asking,
// a permission, an audit entry in its own transaction and a setting of its own.
type greeter struct{ id string }

func (g greeter) ID() string { return g.id }
func (g greeter) Describe() module.Info {
	return module.Info{Name: "module." + g.id + ".name", Description: "module." + g.id + ".description", Notice: "module." + g.id + ".notice"}
}

func (g greeter) Start(_ context.Context, h module.Host) error {
	h.Routes().Handle("GET /hello", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		actor, ok := h.Authz().Actor(ctx)
		if !ok {
			http.Error(w, "nobody", http.StatusUnauthorized)
			return
		}
		admin, err := h.Authz().Allowed(ctx, actor, "settings.manage", module.Target{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		err = h.DB().InTx(ctx, func(tx module.Tx) error {
			return h.Audit().Append(ctx, tx, actor, module.AuditEntry{Action: g.id + ".greet", ObjectKind: "greeting", ObjectID: actor.UserID})
		})
		if err == nil {
			err = h.Settings().Set(ctx, "greeted", json.RawMessage(`"`+actor.UserID+`"`))
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"user": actor.UserID, "admin": admin})
	}))
	return nil
}

// TestModulesOverHTTP: listModules and updateModule switch a module through the host, the
// switch is a setting checked and audited by settings, and while the module is on its routes
// answer under /api/v1/x/<id>/ with the caller, permissions, audit and settings wired to the
// core.
func TestModulesOverHTTP(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{background: true, modules: []module.Module{greeter{"greet"}, greeter{"locked"}}, locked: []string{"locked"}})
	ctx := context.Background()

	f.call(t, http.MethodGet, "/api/v1/modules", "", "", nil, http.StatusUnauthorized)
	f.call(t, http.MethodGet, "/api/v1/modules", testMember, "", nil, http.StatusForbidden)
	rec, _ := f.call(t, http.MethodGet, "/api/v1/modules", testAdmin, "", nil, http.StatusOK)
	var list httpapi.ModuleList
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Items) != 2 || list.Items[0].Id != "greet" || list.Items[0].Enabled || list.Items[0].Locked || !list.Items[1].Locked ||
		list.Items[0].Name != "module.greet.name" || list.Items[0].Notice == nil {
		t.Fatalf("modules: %s", rec.Body)
	}

	hello := func(user string, want int) map[string]any {
		t.Helper()
		rec, _ := f.do(t, http.MethodGet, "/api/v1/x/greet/hello", user, "", nil)
		if rec.Code != want {
			t.Fatalf("hello as %q: %d, want %d\n%s", user, rec.Code, want, rec.Body)
		}
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	hello(testAdmin, http.StatusNotFound) // off

	// A member may not turn it on: refused before the host starts it, and recorded.
	f.call(t, http.MethodPatch, "/api/v1/modules/greet", testMember, `{"enabled":true}`, nil, http.StatusForbidden)
	hello(testAdmin, http.StatusNotFound)
	if rows := auditRows(t, f, settings.ActionModuleEnable); len(rows) != 1 || rows[0].Result != audit.ResultDenied || rows[0].Actor.UserID != testMember {
		t.Errorf("the member's attempt: %+v", rows)
	}

	rec, _ = f.call(t, http.MethodPatch, "/api/v1/modules/greet", testAdmin, `{"enabled":true}`, nil, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Errorf("turned on: %s", rec.Body)
	}
	if ids, err := f.app.settings.EnabledModules(ctx); err != nil || !slices.Equal(ids, []string{"greet"}) {
		t.Errorf("enabled in the settings: %v %v", ids, err)
	}
	if rows := auditRows(t, f, settings.ActionModuleEnable); len(rows) != 2 || rows[0].Result != audit.ResultOK || rows[0].Actor.UserID != testAdmin ||
		rows[0].Object.Label != "module.greet.name" {
		t.Errorf("turning it on is not audited: %+v", rows)
	}

	if got := hello(testAdmin, http.StatusOK); got["user"] != testAdmin || got["admin"] != true {
		t.Errorf("hello as the administrator: %v", got)
	}
	if got := hello(testMember, http.StatusOK); got["user"] != testMember || got["admin"] != false {
		t.Errorf("hello as a member: %v", got)
	}
	hello("", http.StatusUnauthorized)
	if rows := auditRows(t, f, "greet.greet"); len(rows) != 2 {
		t.Errorf("the module's audit entries: %+v", rows)
	}
	if v, ok, err := f.app.settings.ModuleValue(ctx, "modules.greet.greeted"); !ok || err != nil || string(v) != `"`+testMember+`"` {
		t.Errorf("the module's setting: %s %v %v", v, ok, err)
	}
	if rep, err := f.app.audit.Verify(ctx, 1, ""); err != nil || rep.Broken != nil {
		t.Errorf("chain: %+v %v", rep, err)
	}

	f.call(t, http.MethodPatch, "/api/v1/modules/locked", testAdmin, `{"enabled":true}`, nil, http.StatusConflict)
	f.call(t, http.MethodPatch, "/api/v1/modules/nosuch", testAdmin, `{"enabled":true}`, nil, http.StatusNotFound)

	rec, _ = f.call(t, http.MethodPatch, "/api/v1/modules/greet", testAdmin, `{"enabled":false}`, nil, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Errorf("turned off: %s", rec.Body)
	}
	hello(testAdmin, http.StatusNotFound)
	if ids, _ := f.app.settings.EnabledModules(ctx); len(ids) != 0 {
		t.Errorf("still enabled in the settings: %v", ids)
	}
	if rows := auditRows(t, f, settings.ActionModuleDisable); len(rows) != 1 || rows[0].Result != audit.ResultOK {
		t.Errorf("turning it off is not audited: %+v", rows)
	}
}

// TestModulesWithoutBackgroundAre501: the API assembled without the module host (as in tests of
// the API alone) says so instead of listing nothing.
func TestModulesWithoutBackgroundAre501(t *testing.T) {
	f := newAPI(t, false)
	f.call(t, http.MethodGet, "/api/v1/modules", testAdmin, "", nil, http.StatusNotImplemented)
}

func auditRows(t *testing.T, f *apiFixture, action string) []audit.Row {
	t.Helper()
	page, err := f.app.audit.Query(context.Background(), audit.Filter{Action: action}, 200, "")
	if err != nil {
		t.Fatal(err)
	}
	return page.Rows
}
