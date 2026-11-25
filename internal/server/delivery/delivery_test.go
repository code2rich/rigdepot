// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package delivery_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/delivery"
)

// world publishes the resources of the combination test:
//
//   - one skill for each scope (org, shop, personal) and policy (required, recommended,
//     optional): org-req … shop-opt from the repository; me-req, me-rec, me-opt are the member's
//     own, uploaded;
//   - ship, an org-wide required workflow that uses lint (org, optional), review (org,
//     recommended) and shop-tool (shop, optional);
//   - chain, an org-wide optional workflow that uses deep (an optional workflow), which uses
//     leaf (an optional skill): dependencies of dependencies;
//   - loner, an optional workflow in shop that uses org-opt.
func world(t *testing.T, f *fixture) {
	t.Helper()
	files := map[string]string{}
	for _, scope := range []string{"org", "shop"} {
		for _, p := range []string{"required", "recommended", "optional"} {
			files = merge(files, skill(scope+"-"+p[:3], scope, p))
		}
	}
	files = merge(files,
		skill("lint", "org", "optional"), skill("review", "org", "recommended"), skill("shop-tool", "shop", "optional"),
		workflow("ship", "org", "required", "lint", "review", "shop-tool"),
		skill("leaf", "org", "optional"),
		workflow("deep", "org", "optional", "leaf"),
		workflow("chain", "org", "optional", "deep"),
		workflow("loner", "shop", "optional", "org-opt"),
	)
	f.importAndPublish(files)
	for _, p := range []string{"required", "recommended", "optional"} {
		f.personal("member", "me-"+p[:3], p)
	}
}

// got is what a desired state says of each resource: "policy", "enabled" or
// "dependency<-a,b", by code.
func got(st delivery.State) map[string]string {
	out := map[string]string{}
	for _, it := range st.Items {
		v := string(it.Reason)
		if len(it.RequiredBy) > 0 {
			v += "<-" + strings.Join(it.RequiredBy, ",")
		}
		out[it.Resource.Code] = v
	}
	return out
}

// TestDesiredStateCombinations is the acceptance test of M1-27: three scopes × three policies
// × the person's own choices × dependencies coming along.
func TestDesiredStateCombinations(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := newFixture(t, db)
		world(t, f)
		ctx := context.Background()
		prefer := func(name, code string, on bool) {
			t.Helper()
			if _, err := f.svc.SetPreference(ctx, f.as(name), code, on); err != nil {
				t.Fatalf("%s turns %s %v: %v", name, code, on, err)
			}
		}
		// The member turns off two recommended resources (one of them a dependency of a required
		// workflow) and turns on optional ones in the organisation and in shop.
		prefer("member", "org-rec", false)
		prefer("member", "review", false)
		prefer("member", "org-opt", true)
		prefer("member", "shop-opt", true)
		prefer("member", "me-rec", false)
		// The outsider turns on the optional workflow whose dependencies have dependencies.
		prefer("outsider", "chain", true)
		// The owner turns on a product workflow that needs an optional org-wide skill.
		prefer("owner", "loner", true)

		cases := []struct {
			who  string
			code string
			want string // "" means not delivered
		}{
			// The organisation: everyone in it.
			{"member", "org-req", "policy"},
			{"member", "org-rec", ""}, // turned off
			{"member", "org-opt", "enabled"},
			{"outsider", "org-req", "policy"},
			{"outsider", "org-rec", "policy"},
			{"outsider", "org-opt", ""}, // not turned on
			// A product: its members, by the same rules; nobody else.
			{"member", "shop-req", "policy"},
			{"member", "shop-rec", "policy"},
			{"member", "shop-opt", "enabled"},
			{"owner", "shop-req", "policy"},
			{"owner", "shop-opt", ""},
			{"outsider", "shop-req", ""},
			{"outsider", "shop-rec", ""},
			{"admin", "shop-req", ""}, // an administrator who is not in the product
			// Personal: only the author.
			{"member", "me-req", "policy"},
			{"member", "me-rec", ""}, // turned off
			{"member", "me-opt", ""},
			{"owner", "me-req", ""},
			{"admin", "me-req", ""},
			// Dependencies of a required workflow come along whatever their policy or the
			// person's choice; one in a product only for its members.
			{"member", "ship", "policy"},
			{"member", "lint", "dependency<-ship"},
			{"member", "review", "dependency<-ship"},
			{"member", "shop-tool", "dependency<-ship"},
			{"outsider", "lint", "dependency<-ship"},
			{"outsider", "review", "policy"}, // recommended and not turned off: its own reason
			{"outsider", "shop-tool", ""},    // not in shop
			// Dependencies of dependencies; nothing of a workflow nobody turned on.
			{"outsider", "chain", "enabled"},
			{"outsider", "deep", "dependency<-chain"},
			{"outsider", "leaf", "dependency<-deep"},
			{"member", "chain", ""},
			{"member", "deep", ""},
			{"member", "leaf", ""},
			// A product workflow brings an org-wide optional skill.
			{"owner", "loner", "enabled"},
			{"owner", "org-opt", "dependency<-loner"},
		}
		states := map[string]map[string]string{}
		for _, who := range []string{"admin", "owner", "member", "outsider"} {
			st, err := f.svc.Desired(ctx, f.as(who))
			if err != nil {
				t.Fatal(err)
			}
			states[who] = got(st)
			for _, it := range st.Items {
				if it.Live.Version != 1 || !it.Live.Digest.Valid() || !strings.HasPrefix(it.Live.Bundle, "sha256:") || it.Live.Size <= 0 {
					t.Errorf("%s: %s has no live version: %+v", who, it.Resource.Code, it.Live)
				}
			}
		}
		for _, c := range cases {
			if g := states[c.who][c.code]; g != c.want {
				t.Errorf("%s gets %s: %q, want %q", c.who, c.code, g, c.want)
			}
		}
	})
}

func TestPreferences(t *testing.T) {
	f := newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	world(t, f)
	ctx := context.Background()
	if _, err := f.svc.SetPreference(ctx, f.as("member"), "org-req", false); !errors.Is(err, delivery.ErrRequired) {
		t.Errorf("turning off a required resource: %v", err)
	}
	if p, err := f.svc.SetPreference(ctx, f.as("member"), "org-req", true); err != nil || !p.Enabled {
		t.Errorf("turning on a required resource: %+v %v", p, err)
	}
	// Out of sight is not found, for a product and for someone else's personal resource.
	for _, code := range []string{"shop-opt", "me-opt", "no-such"} {
		if _, err := f.svc.SetPreference(ctx, f.as("outsider"), code, true); !errors.Is(err, delivery.ErrNotFound) {
			t.Errorf("outsider turns on %s: %v", code, err)
		}
	}
	p, err := f.svc.SetPreference(ctx, f.as("member"), "org-opt", true)
	if err != nil || !p.Enabled || p.Policy != "optional" {
		t.Fatalf("%+v %v", p, err)
	}
	// The same again changes nothing and records nothing more.
	if _, err := f.svc.SetPreference(ctx, f.as("member"), "org-opt", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetPreference(ctx, f.as("member"), "org-opt", false); err != nil {
		t.Fatal(err)
	}
	page, err := f.log.Query(ctx, audit.Filter{Action: string(delivery.ActionPrefer)}, audit.MaxLimit, "")
	if err != nil || len(page.Rows) != 2 {
		t.Errorf("audit entries: %d, %v", len(page.Rows), err)
	}
	prefs, _ := f.svc.Preferences(ctx, f.users["member"].ID)
	if len(prefs) != 1 {
		t.Errorf("preferences: %v", prefs)
	}
}

// report is a valid report with the given installs.
func report(installs ...delivery.InstallReport) delivery.SyncReport {
	return delivery.SyncReport{CLIVersion: "0.5.0", OS: "darwin", Arch: "arm64", FinishedAt: time.Unix(1764000000, 0), Result: "ok",
		Tools:    []delivery.DetectedTool{{ID: "claude-code", Version: "2.1.330"}, {ID: "codex"}},
		Installs: installs}
}

func TestReportsAndWhereThingsAreInstalled(t *testing.T) {
	f := newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	world(t, f)
	ctx := context.Background()
	laptop, desktop := f.device("member", "laptop"), f.device("member", "desktop")
	other := f.device("outsider", "box")

	if err := f.svc.Report(ctx, f.as("member"), report()); !errors.Is(err, delivery.ErrDenied) {
		t.Errorf("a report without a device: %v", err)
	}
	if err := f.svc.Report(ctx, laptop, report(
		delivery.InstallReport{Code: "org-req", Version: 1, Tool: "claude-code", Status: "installed"},
		delivery.InstallReport{Code: "ship", Version: 1, Tool: "codex", Status: "installed", Degraded: true, Form: "skill"},
		delivery.InstallReport{Code: "lint", Version: 1, Tool: "claude-code", Status: "skipped", Reason: "name_taken"},
		delivery.InstallReport{Code: "not-in-catalog", Version: 3, Tool: "claude-code", Status: "installed"},
	)); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Report(ctx, desktop, report(delivery.InstallReport{Code: "org-req", Version: 1, Tool: "codex", Status: "installed"})); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Report(ctx, other, report(delivery.InstallReport{Code: "org-req", Version: 1, Tool: "claude-code", Status: "failed", Reason: "config_unreadable"})); err != nil {
		t.Fatal(err)
	}
	id := func(code string) string {
		ids, _ := f.cat.ResourceIDs(ctx, []string{code})
		return ids[code]
	}
	people, err := f.svc.InstalledPeople(ctx)
	if err != nil || people[id("org-req")] != 1 || people[id("ship")] != 1 || people[id("lint")] != 0 {
		t.Errorf("installed people: %v %v", people, err)
	}
	if n, _ := f.svc.Installed(ctx, id("org-req")); n[f.users["member"].ID] != 2 || len(n) != 1 {
		t.Errorf("devices per person: %v", n)
	}
	mine, err := f.svc.MyInstalls(ctx, f.users["member"].ID, id("ship"))
	if err != nil || len(mine) != 1 || !mine[0].Degraded || mine[0].Form != "skill" || mine[0].DeviceID != laptop.DeviceID {
		t.Errorf("my installs of ship: %+v %v", mine, err)
	}

	// A newer report replaces the install; removed is no longer installed.
	if err := f.svc.Report(ctx, desktop, report(delivery.InstallReport{Code: "org-req", Version: 1, Tool: "codex", Status: "removed"})); err != nil {
		t.Fatal(err)
	}
	if n, _ := f.svc.Installed(ctx, id("org-req")); n[f.users["member"].ID] != 1 {
		t.Errorf("after removing on the desktop: %v", n)
	}
	// A computer that is signed out no longer counts.
	delete(f.live, laptop.DeviceID)
	if people, _ := f.svc.InstalledPeople(ctx); people[id("org-req")] != 0 {
		t.Errorf("a signed-out computer still counts: %v", people)
	}
	syncs, err := f.svc.DeviceSyncs(ctx, []string{laptop.DeviceID, "unknown"})
	if err != nil || len(syncs) != 1 || syncs[laptop.DeviceID].Result != "ok" || len(syncs[laptop.DeviceID].Tools) != 2 {
		t.Errorf("device syncs: %+v %v", syncs, err)
	}
}

// TestReportsCarryNoPaths: the report is held to codes and versions, so that a path cannot be
// smuggled in where a reason or a tool's version goes.
func TestReportsCarryNoPaths(t *testing.T) {
	f := newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	ctx := context.Background()
	d := f.device("member", "laptop")
	for name, r := range map[string]delivery.SyncReport{
		"reason":       report(delivery.InstallReport{Code: "a", Version: 1, Tool: "codex", Status: "failed", Reason: "/home/jdoe/.codex/config.toml"}),
		"windows path": report(delivery.InstallReport{Code: "a", Version: 1, Tool: "codex", Status: "failed", Reason: `c:\users\jdoe`}),
		"form":         report(delivery.InstallReport{Code: "a", Version: 1, Tool: "codex", Status: "installed", Form: "~/x"}),
		"tool version": {CLIVersion: "0.5.0", OS: "linux", Arch: "amd64", FinishedAt: time.Now(), Result: "ok",
			Tools: []delivery.DetectedTool{{ID: "codex", Version: "/usr/local/bin/codex"}}},
		"status":      report(delivery.InstallReport{Code: "a", Version: 1, Tool: "codex", Status: "broken"}),
		"cli version": {CLIVersion: "latest", OS: "linux", Arch: "amd64", FinishedAt: time.Now(), Result: "ok"},
		"os":          {CLIVersion: "0.5.0", OS: "plan9", Arch: "amd64", FinishedAt: time.Now(), Result: "ok"},
		"version":     report(delivery.InstallReport{Code: "a", Version: 0, Tool: "codex", Status: "installed"}),
		"code":        report(delivery.InstallReport{Code: "../a", Version: 1, Tool: "codex", Status: "installed"}),
	} {
		var e *delivery.Error
		if err := f.svc.Report(ctx, d, r); !errors.As(err, &e) || e.Kind != delivery.KindInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	var n int
	if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM device_syncs`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a refused report was stored: %d %v", n, err)
	}
}

func TestBundlesOnlyForWhoSeesThem(t *testing.T) {
	f := newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	world(t, f)
	ctx := context.Background()
	bundleOf := func(who, code string) string {
		st, err := f.svc.Desired(ctx, f.as(who))
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range st.Items {
			if it.Resource.Code == code {
				return it.Live.Bundle
			}
		}
		t.Fatalf("%s does not get %s", who, code)
		return ""
	}
	shop := bundleOf("member", "shop-req")
	data, err := f.svc.Bundle(ctx, f.as("member"), shop)
	if err != nil || len(data) == 0 {
		t.Fatalf("member fetches a shop bundle: %v", err)
	}
	for who, address := range map[string]string{
		"outsider":     shop,
		"owner":        bundleOf("member", "me-req"),
		"member twice": "sha256:" + strings.Repeat("0", 64),
		"admin":        "not-a-digest",
	} {
		name := strings.Fields(who)[0]
		if _, err := f.svc.Bundle(ctx, f.as(name), address); !errors.Is(err, delivery.ErrNotFound) {
			t.Errorf("%s fetches %s: %v", who, address, err)
		}
	}
}

func TestSummary(t *testing.T) {
	f := newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	world(t, f)
	ctx := context.Background()
	member, outsider := f.device("member", "laptop"), f.device("outsider", "box")
	f.device("owner", "never-synced")
	// The member has every required resource of their scope; the outsider misses ship.
	var installs []delivery.InstallReport
	for _, code := range []string{"org-req", "shop-req", "me-req", "ship"} {
		installs = append(installs, delivery.InstallReport{Code: code, Version: 1, Tool: "claude-code", Status: "installed"})
	}
	if err := f.svc.Report(ctx, member, report(installs...)); err != nil {
		t.Fatal(err)
	}
	r := report(delivery.InstallReport{Code: "org-req", Version: 1, Tool: "codex", Status: "installed"})
	r.CLIVersion, r.Tools = "0.4.2", []delivery.DetectedTool{{ID: "codex"}}
	if err := f.svc.Report(ctx, outsider, r); err != nil {
		t.Fatal(err)
	}

	all, err := f.svc.Summary(ctx, f.as("admin"), "")
	if err != nil {
		t.Fatal(err)
	}
	if all.People != 3 || all.Devices != 3 || all.RequiredComplete != 1 ||
		fmt.Sprint(all.ByTool) != "map[claude-code:1 codex:2]" || fmt.Sprint(all.CLIVersions) != "map[0.4.2:1 0.5.0:1]" {
		t.Errorf("the organisation: %+v", all)
	}
	// The owner sees shop: themselves and the member.
	shop, err := f.svc.Summary(ctx, f.as("owner"), "")
	if err != nil || shop.People != 2 || shop.RequiredComplete != 1 {
		t.Errorf("shop for its owner: %+v %v", shop, err)
	}
	if _, err := f.svc.Summary(ctx, f.as("member"), ""); !errors.Is(err, delivery.ErrDenied) {
		t.Errorf("a member: %v", err)
	}
	if _, err := f.svc.Summary(ctx, f.as("member"), "shop"); !errors.Is(err, delivery.ErrDenied) {
		t.Errorf("a member, for shop: %v", err)
	}
	if _, err := f.svc.Summary(ctx, f.as("admin"), "nope"); !errors.Is(err, delivery.ErrNotFound) {
		t.Errorf("an unknown product: %v", err)
	}
	if s, err := f.svc.Summary(ctx, f.as("admin"), "shop"); err != nil || s.People != 2 {
		t.Errorf("admin, for shop: %+v %v", s, err)
	}
	if got := delivery.Counts(all.ByTool); !slices.Equal(got, []delivery.KeyCount{{Key: "codex", N: 2}, {Key: "claude-code", N: 1}}) {
		t.Errorf("counts: %v", got)
	}
}
