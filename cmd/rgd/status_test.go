// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
)

// status tells the three kinds of drift apart, each with its own lines and exit code: changed on
// this computer (3), deleted (3), cannot be read (9). A sync does not overwrite a change unless
// --overwrite names the resource, and then keeps the person's version in kept/, which status
// lists.
func TestStatusAndDrift(t *testing.T) {
	w := newWorld(t)
	w.must(cliout.ExitOK, "status") // nothing written, no repository: nothing to say
	w.sync(cliout.ExitOK, "--yes-global")
	r := w.must(cliout.ExitOK, "status", "--repo", w.repo)
	inOrder(t, r.stdout, "Resource repository: ", "4 places written by rgd · 4 as written", "Installed", shown(unitDir)+"/")

	// Changed.
	w.write(unitSkill, w.read(unitSkill)+"my own line\n")
	r = w.must(cliout.ExitPending, "status", "--repo", w.repo)
	inOrder(t, r.stdout, "Changed on this computer (a sync does not overwrite it)", "! "+shown(unitDir)+"/", "rgd sync --overwrite unit-testing")
	r = w.must(cliout.ExitPending, "status", "--repo", w.repo, "--diff")
	inOrder(t, r.stdout, shown(unitDir)+"/  (- as the repository has it, + as it is on this computer)", "SKILL.md: changed", "+ my own line")
	doc := document(t, w.must(cliout.ExitPending, "--json", "status", "--repo", w.repo, "--diff"), "rgd.status/v1")
	if s := doc["summary"].(map[string]any); s["modified"] != 1.0 || s["synced"] != 3.0 {
		t.Errorf("summary %v", s)
	}

	// Deleted.
	if err := os.RemoveAll(w.path(reviewDir)); err != nil {
		t.Fatal(err)
	}
	r = w.must(cliout.ExitPending, "status")
	inOrder(t, r.stdout, "No resource repository configured", "Changed on this computer", "Deleted on this computer (a sync does not put it back)", shown(reviewDir)+"/", "rgd sync --overwrite code-review")

	// One resource only.
	r = w.must(cliout.ExitPending, "status", "unit-testing")
	if strings.Contains(r.stdout, "code-review") {
		t.Errorf("status unit-testing shows other resources:\n%s", r.stdout)
	}
	w.must(cliout.ExitUsage, "status", "no-such-resource")

	// A sync leaves both alone and says so.
	r = w.sync(cliout.ExitPending)
	inOrder(t, r.stdout, "Left alone: needs you", "changed on this computer, not overwritten")
	if !strings.Contains(w.read(unitSkill), "my own line") || w.exists(reviewDir) {
		t.Fatal("a sync overwrote what the person changed")
	}
	// --overwrite: put back and overwritten, the person's version kept.
	r = w.sync(cliout.ExitOK, "--overwrite", "unit-testing", "--overwrite", "code-review")
	if strings.Contains(w.read(unitSkill), "my own line") || !w.exists(reviewDir) {
		t.Fatal("--overwrite did not overwrite")
	}
	inOrder(t, r.stdout, "Your changed versions are kept in "+shown("~/"+brand.Home+"/kept/"))
	r = w.must(cliout.ExitOK, "status")
	inOrder(t, r.stdout, "Your changed versions, kept", shown("~/"+brand.Home+"/kept/"), " files")

	// Newer in the repository: --exit-code says there is a difference.
	w.repoWrite("skills/unit-testing/SKILL.md", w.repoRead("skills/unit-testing/SKILL.md")+"new line\n")
	w.commit()
	r = w.must(cliout.ExitOK, "status", "--repo", w.repo)
	inOrder(t, r.stdout, "Newer in the repository (a sync updates it)", shown(unitDir)+"/")
	w.must(cliout.ExitDiff, "status", "--repo", w.repo, "--exit-code")

	// A repository that cannot be reached: status still compares with the manifest.
	r = w.must(cliout.ExitOK, "status", "--repo", w.repo+"-missing")
	if !strings.Contains(r.stderr, "only what was written is compared") {
		t.Errorf("stderr: %s", r.stderr)
	}
}

// An entry that cannot be read has an unknown state: exit code 9, and nothing is concluded about
// it.
func TestStatusOfAnUnreadableEntry(t *testing.T) {
	unixOnly(t)
	w := newWorld(t)
	w.sync(cliout.ExitOK, "--yes-global")
	if err := os.Chmod(w.path(unitDir), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(w.path(unitDir), 0o755) })
	r := w.must(cliout.ExitStateUnknown, "status")
	inOrder(t, r.stdout, "Cannot be read: state unknown", "? "+shown(unitDir)+"/")
	doc := document(t, w.must(cliout.ExitStateUnknown, "--json", "status"), "rgd.status/v1")
	if s := doc["summary"].(map[string]any); s["unreadable"] != 1.0 {
		t.Errorf("summary %v", s)
	}
}
