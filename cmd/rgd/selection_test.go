// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/cliout"
)

// enable and disable: an optional resource is installed only once enabled; a recommended one is
// removed once disabled; a required one cannot be disabled, and the command says why.
func TestEnableAndDisable(t *testing.T) {
	w := newWorld(t)
	w.sync(cliout.ExitOK, "--yes-global")
	if w.exists(releaseDir) {
		t.Fatal("the optional release-notes was installed without being enabled")
	}

	r := w.must(cliout.ExitOK, "enable", "release-notes", "--repo", w.repo)
	inOrder(t, r.stdout, "Turned on release-notes", "rgd sync")
	w.sync(cliout.ExitOK)
	if !w.exists(releaseDir) {
		t.Fatal("enabled, synced, and still not installed")
	}
	r = w.must(cliout.ExitOK, "enable", "release-notes", "--repo", w.repo)
	if !strings.Contains(r.stdout, "already on") {
		t.Errorf("enabling twice:\n%s", r.stdout)
	}

	doc := document(t, w.must(cliout.ExitOK, "--json", "disable", "unit-testing", "--repo", w.repo), "rgd.selection/v1")
	if doc["changed"] != true || doc["policy"] != "recommended" || doc["delivered"] != false {
		t.Errorf("disable unit-testing: %v", doc)
	}
	w.sync(cliout.ExitOK)
	if w.exists(unitDir) {
		t.Fatal("disabled, synced, and still installed")
	}
	w.must(cliout.ExitOK, "enable", "unit-testing", "--repo", w.repo)
	w.sync(cliout.ExitOK)
	if !w.exists(unitDir) {
		t.Fatal("enabled again, synced, and not installed")
	}

	r = w.must(cliout.ExitFailure, "disable", "jira", "--repo", w.repo)
	if !strings.Contains(r.stderr, "required") {
		t.Errorf("disabling a required resource: %s", r.stderr)
	}
	w.lang = "zh-CN"
	r = w.must(cliout.ExitFailure, "disable", "jira", "--repo", w.repo)
	if !strings.Contains(r.stderr, "必装资源，不能停用") {
		t.Errorf("zh-CN: %s", r.stderr)
	}
	w.lang = "en"
	w.must(cliout.ExitUsage, "enable", "no-such-resource", "--repo", w.repo)
	w.must(cliout.ExitUsage, "enable")
}
