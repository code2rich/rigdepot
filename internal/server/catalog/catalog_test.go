// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/catalog"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
)

// skill returns the files of a skill in a repository.
func skill(code, body, meta string) map[string]string {
	if meta == "" {
		meta = "name: Unit testing\nwhen: When a change needs tests\nmaintainers: [outsider]\n"
	}
	return map[string]string{
		"skills/" + code + "/SKILL.md":  "---\nname: " + code + "\ndescription: Writes unit tests.\n---\n" + body,
		"skills/" + code + "/meta.yaml": meta,
	}
}

func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		maps.Copy(out, m)
	}
	return out
}

func mustErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestImportIsIdempotent(t *testing.T) {
	f := newSQLite(t)
	files := merge(skill("unit-testing", "Steps.\n", ""), skill("code-review", "Review.\n", "name: Code review\nwhen: Before merging\n"))
	res := f.importRepo(files, "c1")
	if !slices.Equal(res.Imported, []string{"code-review", "unit-testing"}) || len(res.Failed) > 0 {
		t.Fatalf("first import: %+v", res)
	}
	writes := f.auditCount("resource.create", audit.ResultOK)
	evs := len(f.events.names())

	res = f.importRepo(files, "c1")
	if len(res.Imported) != 0 || len(res.Unchanged) != 2 {
		t.Fatalf("second import: %+v", res)
	}
	if n := f.auditCount("resource.", ""); n != writes {
		t.Errorf("the second import wrote %d audit entries", n-writes)
	}
	if n := len(f.events.names()); n != evs {
		t.Errorf("the second import published %d events", n-evs)
	}
	rs, err := f.svc.List(context.Background(), f.as("admin"), catalog.Filter{})
	if err != nil || len(rs) != 2 || rs[1].Status != catalog.StatusDraft || !rs[1].HasDraft || rs[1].Origin != catalog.OriginRepo {
		t.Fatalf("list: %v %+v", err, rs)
	}
	if rs[1].SourcePath != "skills/unit-testing" || len(rs[1].Maintainers) != 1 || rs[1].Maintainers[0] != f.users["outsider"].ID {
		t.Errorf("unit-testing: %+v", rs[1])
	}
}

func TestImportFailureDoesNotTouchOthersOrDeleteAnything(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	good := skill("unit-testing", "Steps.\n", "")
	f.importRepo(merge(good, skill("code-review", "Review.\n", "")), "c1")
	d, err := f.svc.Draft(ctx, f.as("admin"), "code-review")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Publish(ctx, f.as("admin"), "code-review", d.ETag(), ""); err != nil {
		t.Fatal(err)
	}
	// code-review loses its metadata, unit-testing changes, a third one is new.
	broken := map[string]string{"skills/code-review/SKILL.md": "---\nname: code-review\n---\n"}
	res := f.importRepo(merge(skill("unit-testing", "Steps, changed.\n", ""), broken, skill("release-notes", "Notes.\n", "")), "c2")
	if len(res.Failed) != 1 || res.Failed[0].Code != "code-review" || res.Failed[0].Findings[0].Rule != "structure" {
		t.Fatalf("failed: %+v", res.Failed)
	}
	if !slices.Equal(res.Imported, []string{"release-notes", "unit-testing"}) {
		t.Errorf("imported: %+v", res.Imported)
	}
	r, err := f.svc.Get(ctx, f.as("admin"), "code-review")
	if err != nil || r.Status != catalog.StatusPublished || r.Version != 1 || r.HasDraft {
		t.Errorf("code-review after a failed read: %v %+v", err, r)
	}
	// A repository that lost a resource does not delete it either.
	f.importRepo(skill("unit-testing", "Steps, changed.\n", ""), "c3")
	if _, err := f.svc.Get(ctx, f.as("admin"), "release-notes"); err != nil {
		t.Errorf("release-notes after it left the repository: %v", err)
	}
}

func TestPublishIsRefusedWhileAMustFixFindingStands(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := newFixture(t, db)
		ctx := context.Background()
		f.importRepo(skill("deploy", "Run it.\npassword=hunter2hunter2\n", ""), "c1")
		d, err := f.svc.Draft(ctx, f.as("admin"), "deploy")
		if err != nil {
			t.Fatal(err)
		}
		if n := d.Checks.Counts().Blocking; n != 1 {
			t.Fatalf("blocking = %d: %+v", n, d.Checks)
		}
		_, _, err = f.svc.Publish(ctx, f.as("admin"), "deploy", d.ETag(), "")
		var ce *catalog.Error
		if !errors.As(err, &ce) || ce.Code != "resource.check_blocked" || ce.Params["blocking"] != 1 {
			t.Fatalf("publish with a must-fix finding: %v", err)
		}
		if _, err := f.svc.Version(ctx, f.as("admin"), "deploy", 1); !errors.Is(err, catalog.ErrNotFound) {
			t.Errorf("a version exists after the refusal: %v", err)
		}

		secret := slices.IndexFunc(d.Checks.Findings, func(x catalog.Finding) bool { return x.Rule == "secrets" })
		advisory := slices.IndexFunc(d.Checks.Findings, func(x catalog.Finding) bool { return x.Rule != "secrets" })
		// The maintainer may not mark it; an advisory finding cannot be marked at all.
		if _, err := f.svc.Waive(ctx, f.as("outsider"), "deploy", d.Checks.Findings[secret].ID, "example"); !errors.Is(err, catalog.ErrDenied) {
			t.Errorf("waive as maintainer: %v", err)
		}
		if advisory >= 0 {
			if _, err := f.svc.Waive(ctx, f.as("admin"), "deploy", d.Checks.Findings[advisory].ID, "x"); !errors.Is(err, catalog.ErrNotWaivable) {
				t.Errorf("waive an advisory finding: %v", err)
			}
		}
		w, err := f.svc.Waive(ctx, f.as("admin"), "deploy", d.Checks.Findings[secret].ID, "A documented example")
		if err != nil {
			t.Fatal(err)
		}
		if f.auditCount("resource.waive", audit.ResultOK) != 1 || f.auditCount("resource.waive", audit.ResultDenied) != 1 {
			t.Error("waivers are not in the audit log")
		}
		v, _, err := f.svc.Publish(ctx, f.as("admin"), "deploy", d.ETag(), "first")
		if err != nil || v.Number != 1 {
			t.Fatalf("publish after the waiver: %v %+v", err, v)
		}
		got, _ := f.svc.Version(ctx, f.as("admin"), "deploy", 1)
		if i := slices.IndexFunc(got.Checks.Findings, func(x catalog.Finding) bool { return x.Rule == "secrets" }); got.Checks.Findings[i].Waiver == nil || got.Checks.Findings[i].Waiver.ID != w.ID {
			t.Errorf("the version's finding has no waiver: %+v", got.Checks.Findings)
		}

		// The line changes: the waiver no longer applies.
		f.importRepo(skill("deploy", "Run it.\npassword=hunter3hunter3\n", ""), "c2")
		d, _ = f.svc.Draft(ctx, f.as("admin"), "deploy")
		if d.Checks.Counts().Blocking != 1 {
			t.Errorf("a changed line kept its waiver: %+v", d.Checks)
		}
	})
}

func TestIfMatch(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	f.importRepo(skill("unit-testing", "One.\n", ""), "c1")
	seen, _ := f.svc.Draft(ctx, f.as("admin"), "unit-testing")

	_, _, err := f.svc.Publish(ctx, f.as("admin"), "unit-testing", "", "")
	mustErr(t, err, catalog.ErrPreconditionRequired)
	// The repository moves on between looking and publishing.
	f.importRepo(skill("unit-testing", "Two.\n", ""), "c2")
	_, _, err = f.svc.Publish(ctx, f.as("admin"), "unit-testing", seen.ETag(), "")
	mustErr(t, err, catalog.ErrPreconditionFailed)
	if f.auditCount("resource.publish", "") != 0 {
		t.Error("a refused publish is in the log as done")
	}

	now, _ := f.svc.Draft(ctx, f.as("admin"), "unit-testing")
	if _, _, err := f.svc.Publish(ctx, f.as("admin"), "unit-testing", now.ETag(), ""); err != nil {
		t.Fatal(err)
	}
	r, _ := f.svc.Get(ctx, f.as("admin"), "unit-testing")
	_, err = f.svc.Disable(ctx, f.as("admin"), "unit-testing", `"r-unit-testing-0"`, "gone")
	mustErr(t, err, catalog.ErrPreconditionFailed)
	_, err = f.svc.Disable(ctx, f.as("admin"), "unit-testing", "", "gone")
	mustErr(t, err, catalog.ErrPreconditionRequired)
	off, err := f.svc.Disable(ctx, f.as("admin"), "unit-testing", r.ETag(), "Replaced")
	if err != nil || off.Status != catalog.StatusDisabled || off.ETag() == r.ETag() || off.DisabledReason != "Replaced" {
		t.Fatalf("disable: %v %+v", err, off)
	}
	on, err := f.svc.Enable(ctx, f.as("admin"), "unit-testing", off.ETag())
	if err != nil || on.Status != catalog.StatusPublished {
		t.Fatalf("enable: %v %+v", err, on)
	}
	evs := strings.Join(f.events.names(), " ")
	for _, want := range []string{"catalog.draft_updated", "catalog.published", "catalog.disabled", "catalog.enabled"} {
		if !strings.Contains(evs, want) {
			t.Errorf("no %s in %s", want, evs)
		}
	}
}

func TestVersionsAreInsertOnly(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := newFixture(t, db)
		ctx := context.Background()
		f.importRepo(skill("unit-testing", "One.\n", ""), "c1")
		d, _ := f.svc.Draft(ctx, f.as("admin"), "unit-testing")
		if _, _, err := f.svc.Publish(ctx, f.as("admin"), "unit-testing", d.ETag(), ""); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE resource_versions SET note = 'changed'`); err == nil {
			t.Error("UPDATE of a version succeeded")
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM resource_versions`); err == nil {
			t.Error("DELETE of a version succeeded")
		}
		v, err := f.svc.Version(ctx, f.as("admin"), "unit-testing", 1)
		if err != nil || v.Note != "" {
			t.Errorf("v1: %v %+v", err, v)
		}
	})
}

func TestRollbackPublishesANewVersion(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	publish := func(body, commit string) catalog.Version {
		f.importRepo(skill("unit-testing", body, ""), commit)
		d, err := f.svc.Draft(ctx, f.as("admin"), "unit-testing")
		if err != nil {
			t.Fatal(err)
		}
		v, _, err := f.svc.Publish(ctx, f.as("admin"), "unit-testing", d.ETag(), "")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	v1 := publish("One.\n", "c1")
	v2 := publish("Two.\n", "c2")
	r, _ := f.svc.Get(ctx, f.as("admin"), "unit-testing")

	_, _, err := f.svc.Rollback(ctx, f.as("owner"), "unit-testing", r.ETag(), 1, "")
	mustErr(t, err, catalog.ErrDenied) // an org resource: administrators only
	_, _, err = f.svc.Rollback(ctx, f.as("admin"), "unit-testing", r.ETag(), 7, "")
	mustErr(t, err, catalog.ErrInvalid)
	v3, after, err := f.svc.Rollback(ctx, f.as("admin"), "unit-testing", r.ETag(), 1, "v2 breaks")
	if err != nil {
		t.Fatal(err)
	}
	if v3.Number != 3 || v3.RollbackOf != 1 || v3.Digest != v1.Digest || v3.Bundle != v1.Bundle || after.Version != 3 {
		t.Fatalf("rollback: %+v", v3)
	}
	vs, next, err := f.svc.Versions(ctx, f.as("admin"), "unit-testing", 2, "")
	if err != nil || len(vs) != 2 || vs[0].Number != 3 || next == "" {
		t.Fatalf("versions: %v %+v %q", err, vs, next)
	}
	rest, next, _ := f.svc.Versions(ctx, f.as("admin"), "unit-testing", 2, next)
	if len(rest) != 1 || rest[0].Number != 1 || next != "" || rest[0].Digest != v1.Digest {
		t.Fatalf("second page: %+v %q", rest, next)
	}
	if got, _ := f.svc.Version(ctx, f.as("admin"), "unit-testing", 2); got.Digest != v2.Digest {
		t.Error("v2 changed")
	}
	// The rollback is recorded as one.
	page, _ := f.log.Query(ctx, audit.Filter{Action: "resource.rollback", Result: audit.ResultOK}, 10, "")
	if len(page.Rows) != 1 || !strings.Contains(page.Rows[0].Change, `"rollback_of":1`) {
		t.Errorf("audit: %+v", page.Rows)
	}
}

func TestAnImportEqualToTheLiveVersionLeavesNoDraft(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	f.importRepo(skill("unit-testing", "One.\n", ""), "c1")
	d, _ := f.svc.Draft(ctx, f.as("admin"), "unit-testing")
	f.svc.Publish(ctx, f.as("admin"), "unit-testing", d.ETag(), "")
	f.importRepo(skill("unit-testing", "Bad.\n", ""), "c2")
	f.importRepo(skill("unit-testing", "One.\n", ""), "c3") // reverted
	if _, err := f.svc.Draft(ctx, f.as("admin"), "unit-testing"); !errors.Is(err, catalog.ErrNoDraft) {
		t.Errorf("draft after the repository went back to the live content: %v", err)
	}
	if res := f.importRepo(skill("unit-testing", "One.\n", ""), "c3"); len(res.Unchanged) != 1 {
		t.Errorf("again: %+v", res)
	}
}

func TestPlatformOverrides(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	meta := func(policy string) string {
		return "name: Unit testing\nwhen: Always\npolicy: " + policy + "\nscope: shop\n"
	}
	f.importRepo(skill("unit-testing", "One.\n", meta("optional")), "c1")
	d, _ := f.svc.Draft(ctx, f.as("owner"), "unit-testing")
	if _, _, err := f.svc.Publish(ctx, f.as("owner"), "unit-testing", d.ETag(), ""); err != nil {
		t.Fatal(err)
	}
	r, _ := f.svc.Get(ctx, f.as("owner"), "unit-testing")
	if r.Scope != "shop" || r.Policy != resource.PolicyOptional {
		t.Fatalf("published: %+v", r)
	}
	if _, err := f.svc.Configure(ctx, f.as("member"), "unit-testing", r.ETag(), catalog.Change{}); !errors.Is(err, catalog.ErrNotFound) && !errors.Is(err, catalog.ErrDenied) {
		t.Errorf("a member configures: %v", err)
	}
	required := resource.PolicyRequired
	r, err := f.svc.Configure(ctx, f.as("owner"), "unit-testing", r.ETag(), catalog.Change{Policy: &required})
	if err != nil || r.Policy != required || !slices.Equal(r.Overrides, []string{"policy"}) || len(r.RepositoryDiffers) != 0 {
		t.Fatalf("configure: %v %+v", err, r)
	}
	// The repository changes the policy and the name; publishing applies only the name.
	f.importRepo(skill("unit-testing", "Two.\n", strings.Replace(meta("recommended"), "Unit testing", "Unit tests", 1)), "c2")
	r, _ = f.svc.Get(ctx, f.as("owner"), "unit-testing")
	if !slices.Equal(r.RepositoryDiffers, []string{"policy"}) {
		t.Errorf("repository_differs: %+v", r.RepositoryDiffers)
	}
	d, _ = f.svc.Draft(ctx, f.as("owner"), "unit-testing")
	_, r, err = f.svc.Publish(ctx, f.as("owner"), "unit-testing", d.ETag(), "")
	if err != nil || r.Policy != required || r.Name != "Unit tests" {
		t.Fatalf("published over an override: %v %+v", err, r)
	}
	r, err = f.svc.Configure(ctx, f.as("owner"), "unit-testing", r.ETag(), catalog.Change{Reset: []string{"policy"}})
	if err != nil || r.Policy != resource.PolicyRecommended || len(r.Overrides) != 0 {
		t.Fatalf("reset: %v %+v", err, r)
	}
	page, _ := f.log.Query(ctx, audit.Filter{Action: "resource.configure", Result: audit.ResultOK}, 10, "")
	if len(page.Rows) != 2 || !strings.Contains(page.Rows[1].Change, `"policy":{"from":"optional","to":"required"}`) {
		t.Errorf("audit: %+v", page.Rows)
	}
	unknown := resource.Scope("nope")
	_, err = f.svc.Configure(ctx, f.as("owner"), "unit-testing", r.ETag(), catalog.Change{Scope: &unknown})
	mustErr(t, err, catalog.ErrInvalid)
}

func TestPermissionsAndVisibility(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	f.importRepo(merge(
		skill("org-wide", "Org.\n", "name: Org wide\nwhen: Always\n"),
		skill("shop-only", "Shop.\n", "name: Shop only\nwhen: Always\nscope: shop\n"),
	), "c1")
	// The member manages neither; the owner manages the shop's.
	for _, c := range []struct {
		who, code string
		err       error
	}{{"member", "org-wide", catalog.ErrDenied}, {"member", "shop-only", catalog.ErrDenied}, {"owner", "org-wide", catalog.ErrDenied}, {"owner", "shop-only", nil}} {
		d, err := f.svc.Draft(ctx, f.as("admin"), c.code)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = f.svc.Publish(ctx, f.as(c.who), c.code, d.ETag(), "")
		if !errors.Is(err, c.err) && (c.err != nil || err != nil) {
			t.Errorf("%s publishes %s: %v, want %v", c.who, c.code, err, c.err)
		}
	}
	if n := f.auditCount("resource.publish", audit.ResultDenied); n != 3 {
		t.Errorf("%d refusals recorded, want 3", n)
	}
	d, _ := f.svc.Draft(ctx, f.as("admin"), "org-wide")
	f.svc.Publish(ctx, f.as("admin"), "org-wide", d.ETag(), "")

	names := func(who string) []string {
		rs, err := f.svc.Catalog(ctx, f.as(who), catalog.CatalogFilter{})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rs {
			out = append(out, r.Code)
		}
		return out
	}
	if got := names("member"); !slices.Equal(got, []string{"org-wide", "shop-only"}) {
		t.Errorf("member sees %v", got)
	}
	if got := names("outsider"); !slices.Equal(got, []string{"org-wide"}) {
		t.Errorf("outsider sees %v", got)
	}
	if _, _, err := f.svc.Published(ctx, f.as("outsider"), "shop-only"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("outsider opens shop-only: %v", err)
	}
	if rs, _ := f.svc.List(ctx, f.as("owner"), catalog.Filter{}); len(rs) != 1 || rs[0].Code != "shop-only" {
		t.Errorf("owner manages %+v", rs)
	}
	counts, err := f.svc.ResourceCounts(ctx)
	if err != nil || len(counts) != 1 {
		t.Errorf("counts: %v %v", err, counts)
	}
}

func TestReferencesAndImpact(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	f.importRepo(merge(
		skill("unit-testing", "One.\n", ""),
		map[string]string{"workflows/ship.yaml": "meta: {name: Ship it, when: Before a release}\nsteps:\n  - use: unit-testing\n"},
	), "c1")
	for _, code := range []string{"unit-testing", "ship"} {
		d, _ := f.svc.Draft(ctx, f.as("admin"), code)
		if _, _, err := f.svc.Publish(ctx, f.as("admin"), code, d.ETag(), ""); err != nil {
			t.Fatal(err)
		}
	}
	installed := map[string]int{f.users["member"].ID: 2, f.users["outsider"].ID: 1}
	im, err := f.svc.Impact(ctx, f.as("admin"), "unit-testing", nil, installed)
	if err != nil || im.People != 2 || im.Devices != 3 || len(im.ReferencedBy) != 1 || im.ReferencedBy[0].Code != "ship" {
		t.Fatalf("disable impact: %v %+v", err, im)
	}
	shop := resource.Scope("shop")
	im, _ = f.svc.Impact(ctx, f.as("admin"), "unit-testing", &shop, installed)
	if im.People != 1 || im.Devices != 1 {
		t.Errorf("narrowing to shop: %+v", im)
	}
	r, _ := f.svc.Get(ctx, f.as("admin"), "ship")
	if !slices.Equal(r.References, []string{"unit-testing"}) {
		t.Errorf("references: %+v", r.References)
	}
}

func TestDiscardDraft(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	f.importRepo(skill("unit-testing", "One.\n", ""), "c1")
	d, _ := f.svc.Draft(ctx, f.as("admin"), "unit-testing")
	mustErr(t, f.svc.DiscardDraft(ctx, f.as("admin"), "unit-testing", `"sha256:x"`), catalog.ErrPreconditionFailed)
	if err := f.svc.DiscardDraft(ctx, f.as("admin"), "unit-testing", d.ETag()); err != nil {
		t.Fatal(err)
	}
	// Never published: it is gone.
	if _, err := f.svc.Get(ctx, f.as("admin"), "unit-testing"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("after discarding the only draft: %v", err)
	}
}

// An import keeps when each finding was first seen.
func TestFindingsKeepWhenTheyWereFirstSeen(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	f.importRepo(skill("unit-testing", "One.\n", "name: Unit testing\n"), "c1")
	first, _ := f.svc.Draft(ctx, f.as("admin"), "unit-testing")
	f.importRepo(skill("unit-testing", "Two.\n", "name: Unit testing\n"), "c2")
	second, _ := f.svc.Draft(ctx, f.as("admin"), "unit-testing")
	if len(first.Checks.Findings) != 1 || len(second.Checks.Findings) != 1 || second.Checks.Findings[0].FirstSeen != first.Checks.Findings[0].FirstSeen {
		t.Errorf("first seen: %+v then %+v", first.Checks.Findings, second.Checks.Findings)
	}
}
