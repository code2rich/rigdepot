// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
)

// uninstall --plan lists what would be undone and changes nothing; --tool undoes one tool's
// writes and keeps the local data directory; --keep-home keeps it too.
func TestUninstallPlanToolAndKeepHome(t *testing.T) {
	w := newWorld(t)
	r := w.must(cliout.ExitOK, "uninstall", "--plan")
	if !strings.Contains(r.stdout, "Nothing to undo") || w.exists("~/"+brand.Home) {
		t.Errorf("uninstall --plan before anything was written:\n%s", r.stdout)
	}
	r = w.must(cliout.ExitOK, "uninstall")
	if w.exists("~/" + brand.Home) {
		t.Errorf("uninstall before anything was written created the local data directory:\n%s", r.stdout)
	}

	w.sync(cliout.ExitOK, "--yes-global")
	before := w.snapshot(true)
	r = w.must(cliout.ExitOK, "uninstall", "--plan")
	inOrder(t, r.stdout, "To be undone: 4 places · nothing has been changed yet",
		"- "+shown("~/.otherai/skills/"+brand.Prefix+"jira")+"/",
		"Global configuration: only the keys rgd added are removed", "~ "+shown(settings),
		"Not touched: anything not in the manifest", `Run "rgd uninstall" to undo this`)
	document(t, w.must(cliout.ExitOK, "--json", "uninstall", "--plan"), "rgd.plan/v1")
	if d := diffSnapshots(before, w.snapshot(true)); len(d) > 0 {
		t.Errorf("uninstall --plan changed something: %v", d)
	}

	w.must(cliout.ExitUsage, "uninstall", "--tool", "no-such-tool")
	r = w.must(cliout.ExitOK, "uninstall", "--tool", "otherai")
	if w.exists("~/.otherai/skills/"+brand.Prefix+"jira") || !w.exists(unitDir) {
		t.Errorf("--tool otherai undid the wrong things:\n%s", r.stdout)
	}
	if !w.exists("~/"+brand.Home+"/manifest.json") || !strings.Contains(r.stdout, "other tools' writes are still recorded") {
		t.Errorf("--tool deleted the local data directory:\n%s", r.stdout)
	}

	r = w.must(cliout.ExitOK, "uninstall", "--keep-home")
	if w.exists(unitDir) || !w.exists("~/"+brand.Home+"/manifest.json") {
		t.Errorf("--keep-home:\n%s", r.stdout)
	}
	if w.read(settings) != userConfig {
		t.Errorf("the configuration file is not as it was:\n%s", w.read(settings))
	}
	doc := document(t, w.must(cliout.ExitOK, "--json", "uninstall"), "rgd.uninstall/v1")
	if h := doc["home"].(map[string]any); h["removed"] != true {
		t.Errorf("home %v", h)
	}
	if w.exists("~/" + brand.Home) {
		t.Error("the local data directory is still there")
	}
}

// What people changed is copied to kept/ before it is removed. kept/ is not moved out of the local
// data directory (only the executor writes outside it); the rest of the directory goes and the
// person is told where kept/ is.
func TestUninstallKeepsChangedFiles(t *testing.T) {
	w := newWorld(t)
	w.sync(cliout.ExitOK, "--yes-global")
	w.write(unitSkill, "my version\n")
	r := w.must(cliout.ExitOK, "uninstall")
	inOrder(t, r.stdout, "- "+shown(unitDir)+"/", "a copy goes to", "your changed versions are still in "+shown("~/"+brand.Home+"/kept/"))
	if w.exists(unitDir) {
		t.Fatal("the changed skill was not removed")
	}
	entries, err := os.ReadDir(w.path("~/" + brand.Home))
	if err != nil || len(entries) != 1 || entries[0].Name() != "kept" {
		t.Fatalf("the local data directory holds %v (%v); want only kept/", entries, err)
	}
	var found bool
	filepath.WalkDir(w.path("~/"+brand.Home+"/kept"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(p) == "SKILL.md" {
			data, _ := os.ReadFile(p)
			found = string(data) == "my version\n"
		}
		return nil
	})
	if !found {
		t.Error("the person's version is not in kept/")
	}
}

// uninstall leaves the local data directory when it is not only its own, and never removes what
// it does not know.
func TestUninstallRemovesOnlyItsOwnFiles(t *testing.T) {
	w := newWorld(t)
	w.sync(cliout.ExitOK, "--yes-global")
	w.write("~/"+brand.Home+"/notes.txt", "mine\n")
	r := w.must(cliout.ExitOK, "uninstall")
	if w.read("~/"+brand.Home+"/notes.txt") != "mine\n" || !strings.Contains(r.stdout, "notes.txt") {
		t.Errorf("a file of the person's in the local data directory:\n%s", r.stdout)
	}
}
