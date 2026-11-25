// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
)

// The acceptance test of uninstall: a full sync and an uninstall on a temporary home directory
// leave it byte for byte as it was, the local data directory included; what has the prefix but
// is not in the manifest is not touched.
func TestSyncThenUninstallLeavesTheHomeDirectoryAsItWas(t *testing.T) {
	w := newWorld(t)
	initial := w.snapshot(false)

	w.sync(cliout.ExitOK, "--yes-global")
	if !w.exists(unitSkill) || !w.exists(reviewDir) {
		t.Fatal("the skills were not written")
	}
	if w.exists("~/.otherai/skills/" + brand.Prefix + "unit-testing") {
		t.Error("Other AI got its own copy although it reads Fake Code's directory")
	}
	if !strings.Contains(w.read(settings), brand.Prefix+"notes") || !strings.Contains(w.read(settings), "// mine") {
		t.Errorf("the MCP server was not added next to the person's settings:\n%s", w.read(settings))
	}
	if w.exists(releaseDir) {
		t.Error("an optional resource nobody turned on was installed")
	}

	r := w.must(cliout.ExitOK, "uninstall")
	if d := diffSnapshots(initial, w.snapshot(false)); len(d) > 0 {
		t.Errorf("after sync and uninstall the home directory differs:\n  %s\n%s", strings.Join(d, "\n  "), r.stdout)
	}
	if w.read(mineSkill) != "my own skill\n" {
		t.Error("a skill with the prefix that is not in the manifest was touched")
	}
}

// sync --plan prints the structure of the setup guide's terminal demo (docs/architecture/04-cli.md
// "输出"): the tools found, one line with the total, the groups, what is not changed and what to
// run. It changes nothing in the tools.
func TestSyncPlanShowsWhatWouldBeWrittenAndChangesNothing(t *testing.T) {
	w := newWorld(t)
	before := w.snapshot(true)

	r := w.sync(cliout.ExitOK, "--plan")
	inOrder(t, r.stdout,
		"Detected AI tools: Fake Code · Other AI",
		"To be written: 4 places · nothing has been changed yet",
		"New files and directories (all start with "+brand.Prefix+")",
		"+ "+shown(reviewDir)+"/", "new directory · 1 file", "also read by Other AI",
		"+ "+shown(unitDir)+"/", "new directory · 3 files",
		"Needs your consent: changing global configuration",
		"~ "+shown(settings), "mcp.servers."+brand.Prefix+"notes: new key",
		"Skipped", "Fake Code · jira", "needs a personal secret",
		"Not changed: your own skills",
		`Run "rgd sync --repo `, `--skip-global" writes only the new files`,
	)
	if d := diffSnapshots(before, w.snapshot(true)); len(d) > 0 {
		t.Errorf("--plan changed the tools: %v", d)
	}

	w.lang = "zh-CN"
	r = w.sync(cliout.ExitOK, "--plan")
	inOrder(t, r.stdout, "检测到 AI 工具", "将要写入  共 4 处 · 什么都还没改", "新文件和目录", "需要你同意：修改全局配置", "不会改")

	w.lang = "en"
	w.sync(cliout.ExitDiff, "--plan", "--exit-code")
	r = w.sync(cliout.ExitOK, "--plan", "--json")
	doc := document(t, r, "rgd.plan/v1")
	ops, _ := doc["ops"].([]any)
	consent := 0
	for _, o := range ops {
		op := o.(map[string]any)
		if op["needsConsent"] == true {
			consent++
			if op["kind"] != "key" || op["path"] != settings {
				t.Errorf("needs consent: %v", op)
			}
		}
	}
	if len(ops) != 4 || consent != 1 {
		t.Errorf("ops %d, needing consent %d; want 4 and 1:\n%s", len(ops), consent, r.stdout)
	}

	// Once synced, there is nothing to change.
	w.sync(cliout.ExitOK, "--yes-global")
	r = w.sync(cliout.ExitOK, "--plan", "--exit-code")
	if !strings.Contains(r.stdout, "Nothing to change") {
		t.Errorf("after the sync:\n%s", r.stdout)
	}
}

// Without a terminal and without --yes-global, no tool's global configuration is changed: the
// rest is written and the exit code is 3. --yes-global counts only on the command line.
func TestGlobalConfigurationNeedsConsent(t *testing.T) {
	w := newWorld(t)

	r := w.sync(cliout.ExitPending)
	if w.read(settings) != userConfig {
		t.Errorf("the global configuration was changed without consent:\n%s", w.read(settings))
	}
	if !w.exists(unitSkill) {
		t.Error("the new files were not written")
	}
	inOrder(t, r.stdout, "Waiting for your consent: not changed", shown(settings), "--yes-global")
	if w.exists("~/" + brand.Home + "/state/consent.json") {
		t.Error("a consent was recorded although nobody agreed")
	}

	w.sync(cliout.ExitPending, "--skip-global")
	w.writeConfig("yes_global = true\n")
	r = w.sync(cliout.ExitPending)
	if w.read(settings) != userConfig || !strings.Contains(r.stderr, "--yes-global") {
		t.Errorf("yes_global in the configuration file counted: %s", r.stderr)
	}
	w.writeConfig("")

	// A person at a terminal is asked, file by file.
	w.terminal, w.stdin = true, "n\n"
	r = w.sync(cliout.ExitPending)
	if !strings.Contains(r.stderr, "Change "+shown(settings)+" for Fake Code") || w.read(settings) != userConfig {
		t.Errorf("answering no:\nstderr: %s\n%s", r.stderr, w.read(settings))
	}
	w.stdin = "y\n"
	w.sync(cliout.ExitPending, "--non-interactive")
	if w.read(settings) != userConfig {
		t.Error("--non-interactive asked anyway")
	}
	w.sync(cliout.ExitOK)
	if !strings.Contains(w.read(settings), brand.Prefix+"notes") {
		t.Errorf("answering yes did not change the file:\n%s", w.read(settings))
	}
	if !w.exists("~/" + brand.Home + "/state/consent.json") {
		t.Error("the consent was not recorded")
	}

	// --yes-global on its own.
	w2 := newWorld(t)
	w2.sync(cliout.ExitOK, "--yes-global")
	if !strings.Contains(w2.read(settings), brand.Prefix+"notes") {
		t.Error("--yes-global did not change the file")
	}
	w2.must(cliout.ExitUsage, "sync", "--repo", w2.repo, "--yes-global", "--skip-global")
	w2.must(cliout.ExitUsage, "sync", "--repo", w2.repo, "--exit-code")
}

// What cannot be had or read stops the sync before anything is changed, with its exit code.
func TestSyncFailures(t *testing.T) {
	w := newWorld(t)
	before := w.snapshot(false)
	w.must(cliout.ExitUsage, "sync")                                    // no repository
	w.must(cliout.ExitUnreachable, "sync", "--repo", w.repo+"-missing") // unreachable
	w.sync(cliout.ExitUsage, "--tool", "no-such-tool")
	if d := diffSnapshots(before, w.snapshot(true)); len(d) > 0 {
		t.Errorf("failed syncs changed the tools: %v", d)
	}

	// A manifest that cannot be read is unknown, not empty: nothing is written.
	w.sync(cliout.ExitOK, "--yes-global")
	w.write("~/"+brand.Home+"/manifest.json", `{"schema":1,"entries":[`)
	w.write(unitSkill, "changed\n")
	r := w.run("--json", "sync", "--repo", w.repo, "--yes-global", "--overwrite", "unit-testing")
	if r.code != int(cliout.ExitStateUnknown) {
		t.Fatalf("exit code %d, want 9\n%s", r.code, r.stdout)
	}
	doc := document(t, r, "rgd.error/v1")
	if e := doc["error"].(map[string]any); e["code"] != "manifest_unreadable" {
		t.Errorf("error %v", e)
	}
	if w.read(unitSkill) != "changed\n" {
		t.Error("written although the manifest cannot be read")
	}
	w.must(cliout.ExitStateUnknown, "uninstall")
	w.must(cliout.ExitStateUnknown, "status")
}

// The repository named in the configuration is used without --repo.
func TestSyncFromTheConfiguredRepository(t *testing.T) {
	w := newWorld(t)
	w.writeConfig("[repo]\nurl = " + strconvQuote(w.repo) + "\n")
	w.must(cliout.ExitOK, "sync", "--yes-global")
	if !w.exists(unitSkill) {
		t.Error("nothing was synced from repo.url")
	}
}
