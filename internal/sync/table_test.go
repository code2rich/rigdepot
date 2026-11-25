// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"os"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/executor"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// One test per row of the table in docs/architecture/05-adapters.md, "计划、应用、撤销、漂移".

var (
	unitDir   = "~/.fakecode/skills/" + brand.Prefix + "unit-testing"
	unitSkill = unitDir + "/SKILL.md"
	unitID    = manifest.EntryID("fakecode", manifest.OpTree, unitDir, nil)
)

// synced is a world in which unit-testing (v1) was synced once.
func synced(t *testing.T) (*world, repo) {
	t.Helper()
	w := newWorld(t)
	r := newRepo().skill("unit-testing", "v1\n")
	w.run(r.state(t, desired.Filter{}), Options{}, ConsentInput{})
	if w.read(unitSkill) == "" {
		t.Fatal("the first sync wrote nothing")
	}
	return w, r
}

func entry(t *testing.T, w *world, id string) (manifest.Entry, bool) {
	t.Helper()
	return w.manifest().Find(id)
}

// Row 1: not in the manifest, not on disk, in the plan: create (+).
func TestRowNewIsCreated(t *testing.T) {
	w := newWorld(t)
	st := newRepo().skill("unit-testing", "v1\n").state(t, desired.Filter{})
	p, res := w.run(st, Options{}, ConsentInput{})
	if it := item(t, p, "fakecode", unitDir); it.Action != ActionCreate || it.Entry != nil {
		t.Errorf("planned %s (entry %v), want %s", it.Action, it.Entry, ActionCreate)
	}
	if !strings.HasSuffix(w.read(unitSkill), "v1\n") || len(res.Done) != 1 {
		t.Errorf("the skill was not written: %+v", res)
	}
	en, ok := entry(t, w, unitID)
	if !ok || en.Resource != "unit-testing" || !strings.HasPrefix(en.Digest, "sha256:") {
		t.Errorf("manifest entry = %+v, %v", en, ok)
	}
	// The manifest says where the resources came from.
	if m := w.manifest(); m.Source == nil || m.Source.Type != "repo" || m.Source.URL != "https://git.example.com/resources.git" {
		t.Errorf("manifest source = %+v", m.Source)
	}
}

// Row 2: not in the manifest, on disk, in the plan: the person's own; left alone (! taken).
func TestRowTakenIsLeftAlone(t *testing.T) {
	w := newWorld(t)
	w.write(unitSkill, "the person's own unit-testing skill\n")
	before := w.snapshot()
	st := newRepo().skill("unit-testing", "v1\n").state(t, desired.Filter{})
	p, res := w.run(st, Options{}, ConsentInput{})
	if it := item(t, p, "fakecode", unitDir); it.Action != ActionTaken || !it.Action.Attention() {
		t.Errorf("planned %s, want %s", it.Action, ActionTaken)
	}
	if len(p.Attention()) != 1 || len(res.Done) != 0 {
		t.Errorf("attention %v, done %v", p.Attention(), res.Done)
	}
	if d := changes(before, w.snapshot()); len(d) > 0 {
		t.Errorf("the person's own skill was touched: %v", d)
	}
	if _, ok := entry(t, w, unitID); ok {
		t.Error("the person's own skill was put in the manifest")
	}
}

// Row 3: in the manifest, as recorded, the plan wants the same: nothing to do, nothing shown.
func TestRowUnchangedDoesNothing(t *testing.T) {
	w, r := synced(t)
	before := w.snapshot()
	p, res := w.run(r.state(t, desired.Filter{}), Options{}, ConsentInput{})
	if it := item(t, p, "fakecode", unitDir); it.Action != ActionNone {
		t.Errorf("planned %s, want %s", it.Action, ActionNone)
	}
	if len(p.Changes()) != 0 || len(res.Done) != 0 || len(changes(before, w.snapshot())) != 0 {
		t.Errorf("a second sync was not empty: changes %v, done %v", p.Changes(), res.Done)
	}
}

// Row 4: in the manifest, as recorded, the plan wants something else: update (~).
func TestRowNewContentIsUpdated(t *testing.T) {
	w, r := synced(t)
	old, _ := entry(t, w, unitID)
	p, _ := w.run(r.skill("unit-testing", "v2\n").state(t, desired.Filter{}), Options{}, ConsentInput{})
	it := item(t, p, "fakecode", unitDir)
	if it.Action != ActionUpdate {
		t.Errorf("planned %s, want %s", it.Action, ActionUpdate)
	}
	if !strings.HasSuffix(w.read(unitSkill), "v2\n") {
		t.Error("the skill was not updated")
	}
	if en, _ := entry(t, w, unitID); en.Digest == old.Digest || en.Digest != it.Want {
		t.Errorf("the manifest digest is %s; it was %s and should be %s", en.Digest, old.Digest, it.Want)
	}
}

// Row 5: in the manifest, changed on disk: not overwritten (! modified); with --overwrite it is,
// and the person's version is kept.
func TestRowChangedHereIsNotOverwritten(t *testing.T) {
	w, r := synced(t)
	w.write(unitSkill, "edited by hand\n")
	st := r.skill("unit-testing", "v2\n").state(t, desired.Filter{})

	p, res := w.run(st, Options{}, ConsentInput{})
	if it := item(t, p, "fakecode", unitDir); it.Action != ActionModified {
		t.Errorf("planned %s, want %s", it.Action, ActionModified)
	}
	if w.read(unitSkill) != "edited by hand\n" || len(res.Done) != 0 {
		t.Error("a skill changed by hand was overwritten without --overwrite")
	}

	p, res = w.run(st, Options{Overwrite: []string{"unit-testing"}}, ConsentInput{})
	if it := item(t, p, "fakecode", unitDir); it.Action != ActionOverwrite {
		t.Errorf("with --overwrite planned %s, want %s", it.Action, ActionOverwrite)
	}
	if !strings.HasSuffix(w.read(unitSkill), "v2\n") || len(res.Done) != 1 || res.Done[0].Kept == "" {
		t.Fatalf("--overwrite did not replace and keep: %+v", res.Done)
	}
	kept, err := os.ReadFile(w.data.Join(res.Done[0].Kept, "SKILL.md"))
	if err != nil || string(kept) != "edited by hand\n" {
		t.Errorf("kept/ has %q, %v; want the version edited by hand", kept, err)
	}
}

// Row 6: in the manifest, changed on disk, but exactly to what the plan wants: only the manifest
// changes, nothing is shown.
func TestRowChangedToTheWantedContentIsOnlyRecorded(t *testing.T) {
	w, r := synced(t)
	st := r.skill("unit-testing", "v2\n").state(t, desired.Filter{})
	w.write(unitSkill, r["skills/unit-testing/SKILL.md"])
	before := w.snapshot()
	p, _ := w.run(st, Options{}, ConsentInput{})
	it := item(t, p, "fakecode", unitDir)
	if it.Action != ActionRecord || it.Action.Changes() {
		t.Errorf("planned %s, want %s", it.Action, ActionRecord)
	}
	if d := changes(before, w.snapshot()); len(d) > 0 {
		t.Errorf("files changed: %v", d)
	}
	if en, _ := entry(t, w, unitID); en.Digest != it.Want {
		t.Errorf("the manifest digest is %s, want %s", en.Digest, it.Want)
	}
	if p2 := w.plan(st, Options{}); item(t, p2, "fakecode", unitDir).Action != ActionNone {
		t.Error("after recording, the next plan still has something to do")
	}
}

// Row 7: in the manifest, deleted on disk: not put back (! deleted); --overwrite puts it back.
func TestRowDeletedHereIsNotRestored(t *testing.T) {
	w, r := synced(t)
	if err := os.RemoveAll(w.path(unitDir)); err != nil {
		t.Fatal(err)
	}
	st := r.state(t, desired.Filter{})
	p, _ := w.run(st, Options{}, ConsentInput{})
	if it := item(t, p, "fakecode", unitDir); it.Action != ActionDeleted {
		t.Errorf("planned %s, want %s", it.Action, ActionDeleted)
	}
	if w.exists(unitDir) {
		t.Error("a skill deleted by hand was put back without --overwrite")
	}
	if _, ok := entry(t, w, unitID); !ok {
		t.Error("the entry of a skill deleted by hand was dropped; status could not report it")
	}
	p, _ = w.run(st, Options{Overwrite: []string{"unit-testing"}}, ConsentInput{})
	if it := item(t, p, "fakecode", unitDir); it.Action != ActionRestore {
		t.Errorf("with --overwrite planned %s, want %s", it.Action, ActionRestore)
	}
	if !w.exists(unitSkill) {
		t.Error("--overwrite did not put the skill back")
	}
}

// Row 8: in the manifest, not in the plan: undo (-). Also when the resource is still in the
// repository but no longer for this person (an optional resource they did not enable).
func TestRowNoLongerWantedIsUndone(t *testing.T) {
	for name, next := range map[string]func(t *testing.T, r repo) *desired.State{
		"removed from the repository": func(t *testing.T, r repo) *desired.State {
			return r.without("unit-testing").state(t, desired.Filter{})
		},
		"optional, not enabled": func(t *testing.T, r repo) *desired.State {
			r["skills/unit-testing/meta.yaml"] = "name: unit-testing\npolicy: optional\n"
			return r.state(t, desired.Filter{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			w, r := synced(t)
			p, res := w.run(next(t, r), Options{}, ConsentInput{})
			it := item(t, p, "fakecode", unitDir)
			if it.Action != ActionRevert || it.Entry == nil {
				t.Errorf("planned %s, want %s", it.Action, ActionRevert)
			}
			if w.exists(unitDir) || len(res.Done) != 1 || res.Done[0].Outcome != executor.Removed {
				t.Errorf("the skill was not undone: %+v", res.Done)
			}
			if _, ok := entry(t, w, unitID); ok {
				t.Error("the entry is still in the manifest")
			}
			// The skills directory existed before the first sync (the person's own skill is in
			// it) and stays.
			if !w.exists("~/.fakecode/skills/" + brand.Prefix + "mine/SKILL.md") {
				t.Error("the person's own skill went with ours")
			}
		})
	}
}

// Row 9: something cannot be read: the tool is left out of this run, nothing is written or undone.
func TestRowUnreadableLeavesTheToolAlone(t *testing.T) {
	unixOnly(t)
	w, r := synced(t)
	r.skill("review", "v1\n") // something new to write …
	st := r.skill("unit-testing", "v2\n").state(t, desired.Filter{})
	ref := w.path(unitDir + "/references")
	w.write(unitDir+"/references/a.md", "x\n")
	if err := os.Chmod(ref, 0); err != nil { // … and a target that cannot be read
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ref, 0o755) })
	before := w.snapshot()

	p, res := w.run(st, Options{}, ConsentInput{})
	tl := toolOf(t, p, "fakecode")
	if tl.Status != ToolUnreadable || !strings.Contains(tl.Reason, unitDir) {
		t.Errorf("tool status %s (%s), want %s naming %s", tl.Status, tl.Reason, ToolUnreadable, unitDir)
	}
	if len(p.Items) != 0 || len(res.Done) != 0 {
		t.Errorf("an unreadable tool still has items: %+v", p.Items)
	}
	if d := changes(before, w.snapshot()); len(d) > 0 {
		t.Errorf("files changed: %v", d)
	}
}

// The adapter's own "cannot read the configuration" is the same row: the adapter still plans the
// skills, and the core writes nothing at all to the tool.
func TestRowConfigUnreadableLeavesTheToolAlone(t *testing.T) {
	w, r := synced(t)
	w.write("~/.fakecode/settings.jsonc", "{\n  \"mcp\": {\n}\n") // cut off
	r.skill("unit-testing", "v2\n").skill("review", "v1\n")
	r["servers/jira/server.txt"] = "jira-mcp\n"
	r["servers/jira/meta.yaml"] = "name: jira\n"
	before := w.snapshot()
	p, _ := w.run(r.state(t, desired.Filter{}), Options{}, ConsentInput{YesGlobal: true})
	if tl := toolOf(t, p, "fakecode"); tl.Status != ToolUnreadable || !strings.Contains(tl.Reason, "settings.jsonc") {
		t.Errorf("tool = %+v", tl)
	}
	if len(p.Items) != 0 || len(changes(before, w.snapshot())) != 0 {
		t.Errorf("an unreadable configuration still led to items %+v", p.Items)
	}
}
