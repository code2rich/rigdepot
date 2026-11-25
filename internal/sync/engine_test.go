// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/executor"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// undetectable is an adapter that cannot tell whether its tool is there.
type undetectable struct{ adapter.Adapter }

func (u undetectable) Detect(context.Context, adapter.Env) (adapter.Detection, error) {
	return adapter.Detection{Status: adapter.Unknown, Reason: "adapter.detect.unreadable"}, nil
}

var (
	flowCmd   = "~/.fakecode/commands/" + brand.Prefix + "release.md"
	otherFlow = "~/.otherai/skills/" + brand.Prefix + "release"
	otherUnit = "~/.otherai/skills/" + brand.Prefix + "unit-testing"
)

func flowRepo() repo {
	r := newRepo().skill("unit-testing", "v1\n")
	r["flows/release/FLOW.md"] = "1. Tag.\n2. Build.\n"
	r["flows/release/meta.yaml"] = "name: release\n"
	return r
}

// A form the tool cannot take falls back to the next one, and the plan says so; the adapter of a
// tool that reads another tool's skill directory does not get a second copy of a skill.
func TestFallbackAndSharedDirectories(t *testing.T) {
	w := newWorld(t, fakeCode(), otherAI())
	p, _ := w.run(flowRepo().state(t, desired.Filter{}), Options{}, ConsentInput{})

	// Fake Code takes commands: the flow is one. Other AI does not: the flow is a skill.
	if it := item(t, p, "fakecode", flowCmd); it.Action != ActionCreate || it.Degraded != "" {
		t.Errorf("Fake Code: %s degraded %q", it.Action, it.Degraded)
	}
	it := item(t, p, "otherai", otherFlow)
	if it.Action != ActionCreate || it.Degraded != "workflow.as_skill" {
		t.Errorf("Other AI: %s degraded %q, want a skill marked workflow.as_skill", it.Action, it.Degraded)
	}
	other := toolOf(t, p, "otherai")
	if len(other.Degraded) != 1 || other.Degraded[0].Note != "workflow.as_skill" {
		t.Errorf("Other AI's degraded list = %+v", other.Degraded)
	}
	if !w.exists(otherFlow + "/SKILL.md") {
		t.Error("the flow did not reach Other AI as a skill")
	}

	// Other AI reads ~/.fakecode/skills: it gets no copy of unit-testing of its own.
	if w.exists(otherUnit) || slices.ContainsFunc(p.Items, func(it Item) bool { return it.Target.Path == otherUnit }) {
		t.Error("Other AI got its own copy of a skill it reads from Fake Code's directory")
	}
	if len(other.Shared) != 1 || other.Shared[0].Via != "fakecode" || other.Shared[0].Path != unitDir {
		t.Errorf("Other AI's shared list = %+v", other.Shared)
	}
	if it := item(t, p, "fakecode", unitDir); !slices.Equal(it.SharedWith, []string{"otherai"}) {
		t.Errorf("Fake Code's item is shared with %v", it.SharedWith)
	}
	en, _ := w.manifest().Find(unitID)
	if !slices.Equal(en.SharedWith, []string{"otherai"}) {
		t.Errorf("the manifest entry is shared with %v, want [otherai]", en.SharedWith)
	}

	// Once Fake Code is gone from the run, Other AI needs its own copy again.
	p, _ = w.run(flowRepo().state(t, desired.Filter{}), Options{Tools: []string{"otherai"}}, ConsentInput{})
	if it := item(t, p, "otherai", otherUnit); it.Action != ActionCreate {
		t.Errorf("Other AI alone: %s", it.Action)
	}
	if tl := toolOf(t, p, "fakecode"); tl.Status != ToolNotSelected {
		t.Errorf("Fake Code: %s", tl.Status)
	}
	if !w.exists(unitSkill) {
		t.Error("a tool left out with --tool lost what it had")
	}
}

// What was written for a resource whose state this run is unknown stays: one the repository
// cannot read now, and one the adapter skipped.
func TestUnknownResourcesAreHeldNotUndone(t *testing.T) {
	w, r := synced(t)
	delete(r, "skills/unit-testing/SKILL.md") // the resource fails to parse
	p, res := w.run(r.state(t, desired.Filter{}), Options{}, ConsentInput{})
	if slices.ContainsFunc(p.Items, func(it Item) bool { return it.Target.Path == unitDir }) || len(res.Done) != 0 {
		t.Errorf("a resource the repository could not read was acted on: %+v", p.Items)
	}
	if tl := toolOf(t, p, "fakecode"); !slices.Contains(tl.Held, "unit-testing") {
		t.Errorf("held = %v", tl.Held)
	}
	if !w.exists(unitSkill) {
		t.Error("the skill was removed")
	}
}

// A desired state kept from earlier, because its source cannot be reached, removes nothing it
// does not deliver (Options.Keep); the same state from a source that answered does.
func TestAKeptStateRemovesNothing(t *testing.T) {
	w, r := synced(t)
	delete(r, "skills/unit-testing/SKILL.md")
	delete(r, "skills/unit-testing/meta.yaml")
	st := r.state(t, desired.Filter{})
	if _, ok := st.Lookup("unit-testing"); ok {
		t.Fatal("the resource is still in the state")
	}
	p, res := w.run(st, Options{Keep: true}, ConsentInput{})
	if len(p.Changes()) != 0 || len(res.Done) != 0 || !w.exists(unitSkill) {
		t.Fatalf("a kept state undid something: %+v", p.Items)
	}
	p, _ = w.run(st, Options{}, ConsentInput{})
	if it := item(t, p, "fakecode", unitDir); it.Action != ActionRevert || w.exists(unitSkill) {
		t.Errorf("without Keep: %s, still there %v", it.Action, w.exists(unitSkill))
	}
}

// A tool whose detection cannot tell is left alone, and so is one the configuration disables.
func TestToolsThatDoNotTakePart(t *testing.T) {
	w, r := synced(t)
	st := r.without("unit-testing").state(t, desired.Filter{})

	w.tools = []adapter.Adapter{undetectable{fakeCode()}}
	w.eng = w.engine()
	p, _ := w.run(st, Options{}, ConsentInput{})
	if tl := toolOf(t, p, "fakecode"); tl.Status != ToolUnknown || tl.Reason != "adapter.detect.unreadable" || len(p.Items) != 0 {
		t.Errorf("undetectable: %+v, items %v", tl, p.Items)
	}

	w.tools = []adapter.Adapter{fakeCode()}
	w.eng = w.engine()
	p, _ = w.run(st, Options{Disabled: []string{"fakecode"}}, ConsentInput{})
	if tl := toolOf(t, p, "fakecode"); tl.Status != ToolDisabled || len(p.Items) != 0 {
		t.Errorf("disabled: %+v, items %v", tl, p.Items)
	}
	if !w.exists(unitSkill) {
		t.Error("the skill of a tool that took no part was removed")
	}
	if _, err := w.eng.Plan(ctx, st, Options{Tools: []string{"nosuchtool"}}); !errors.Is(err, ErrUnknownTool) {
		t.Errorf("--tool nosuchtool: %v", err)
	}
}

// The consent rule: global configuration changes need the person's consent the first time per
// file. Without a terminal and without --yes-global they wait, and nothing else waits for them.
func TestConsentForGlobalConfiguration(t *testing.T) {
	w := newWorld(t)
	r := newRepo().skill("unit-testing", "v1\n")
	r["servers/jira/server.txt"] = "jira-mcp\n"
	r["servers/jira/meta.yaml"] = "name: jira\n"
	st := r.state(t, desired.Filter{})
	jira := "~/.fakecode/settings.jsonc"

	// No terminal, no --yes-global: the skill is written, the key waits.
	p, res := w.run(st, Options{}, ConsentInput{})
	it := item(t, p, "fakecode", jira)
	if it.Action != ActionCreate || !it.Global {
		t.Fatalf("the MCP server: %s global=%v", it.Action, it.Global)
	}
	if len(p.Consents) != 1 || p.Consents[0].File != jira || p.Consents[0].Granted || p.Summary().NeedsConsent != 1 {
		t.Errorf("consents = %+v", p.Consents)
	}
	if !res.Attention() || len(res.Deferred) != 1 || !w.exists(unitSkill) {
		t.Errorf("deferred %v, attention %v", res.Deferred, res.Attention())
	}
	if w.exists(ConsentFileFor(w)) {
		t.Error("a consent was recorded that nobody gave")
	}

	// --skip-global, even with --yes-global: nothing global.
	_, res = w.run(st, Options{}, ConsentInput{SkipGlobal: true, YesGlobal: true})
	if len(res.Deferred) != 1 {
		t.Errorf("--skip-global: deferred %v", res.Deferred)
	}

	// A person at a terminal says no, then yes; asked once per file.
	var asked []string
	ask := func(answer bool) func(Consent) bool {
		return func(c Consent) bool { asked = append(asked, c.File); return answer }
	}
	_, res = w.run(st, Options{}, ConsentInput{Interactive: true, Ask: ask(false)})
	if len(res.Deferred) != 1 || !slices.Equal(asked, []string{jira}) {
		t.Errorf("said no: deferred %v, asked %v", res.Deferred, asked)
	}
	_, res = w.run(st, Options{}, ConsentInput{Interactive: true, Ask: ask(true)})
	if len(res.Deferred) != 0 || len(res.Done) != 1 {
		t.Errorf("said yes: deferred %v, done %v", res.Deferred, res.Done)
	}
	if _, ok := w.manifest().Find(it.ID); !ok {
		t.Error("the key is not in the manifest")
	}

	// From now on the same file is not asked about again, with or without a terminal.
	r["servers/wiki/server.txt"] = "wiki-mcp\n"
	r["servers/wiki/meta.yaml"] = "name: wiki\n"
	asked = nil
	p, res = w.run(r.state(t, desired.Filter{}), Options{}, ConsentInput{Interactive: true, Ask: ask(false)})
	if len(asked) != 0 || len(res.Deferred) != 0 || len(p.Consents) != 1 || !p.Consents[0].Granted {
		t.Errorf("a file agreed to before: asked %v, deferred %v, consents %+v", asked, res.Deferred, p.Consents)
	}

	// --yes-global agrees for the files of this plan and is remembered too.
	w2 := newWorld(t)
	_, res = w2.run(st, Options{}, ConsentInput{YesGlobal: true})
	if len(res.Deferred) != 0 || !w2.exists(ConsentFileFor(w2)) {
		t.Errorf("--yes-global: deferred %v", res.Deferred)
	}

	// An uninstall needs no consent: the person asked for exactly that.
	up, err := w.eng.PlanUninstall(ctx, UninstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ap := up.Approve(ConsentInput{}, fixedNow()); !ap.Approved(item(t, up, "fakecode", jira)) {
		t.Error("uninstalling a key needs consent")
	}
}

// ConsentFileFor returns the consent file of a world as a "~/" path.
func ConsentFileFor(w *world) string { return "~/" + brand.Home + "/" + ConsentFile }

// A consent file that cannot be read counts as no consent, and is not overwritten.
func TestAnUnreadableConsentFileIsNoConsent(t *testing.T) {
	w := newWorld(t)
	r := newRepo()
	r["servers/jira/server.txt"] = "jira-mcp\n"
	r["servers/jira/meta.yaml"] = "name: jira\n"
	w.write(ConsentFileFor(w), `{"schema": 1, "files": [{"tool": "fakecode", "file": "~/.fakecode/settings.jsonc"`)
	before := w.read(ConsentFileFor(w))
	p, res := w.run(r.state(t, desired.Filter{}), Options{}, ConsentInput{YesGlobal: true})
	if p.Consents[0].Granted {
		t.Error("a consent was read from a broken file")
	}
	if len(res.Deferred) != 0 {
		t.Error("--yes-global did not count for this run")
	}
	if w.read(ConsentFileFor(w)) != before {
		t.Error("the broken consent file was overwritten")
	}
}

// Preview and write: the same operations, the same changes, for a run that creates, updates and
// undoes. (run checks it for every sync of these tests; this one makes sure there was something
// to compare.)
func TestPreviewAndWriteAreTheSameOperations(t *testing.T) {
	w := newWorld(t, fakeCode(), otherAI())
	w.run(flowRepo().state(t, desired.Filter{}), Options{}, ConsentInput{})
	r := flowRepo().skill("unit-testing", "v2\n").without("release").skill("review", "v1\n")
	w.write(unitDir+"/notes.md", "by hand\n")
	before := w.snapshot()
	s, err := w.eng.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release()
	p := w.plan(r.state(t, desired.Filter{}), Options{Overwrite: []string{"unit-testing"}})
	w.log.reset()
	prev, err := w.eng.Preview(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if d := changes(before, w.snapshot()); len(d) > 0 {
		t.Fatalf("the preview changed the home directory: %v", d)
	}
	if _, err := s.Apply(ctx, p, p.Approve(ConsentInput{}, fixedNow())); err != nil {
		t.Fatal(err)
	}
	pv, rl := w.log.get("preview"), w.log.get("real")
	if len(rl) < 4 || !slices.Equal(pv, rl) {
		t.Errorf("operations differ\npreview:\n  %s\nwritten:\n  %s", strings.Join(pv, "\n  "), strings.Join(rl, "\n  "))
	}
	got, want := previewChanges(prev), changes(before, w.snapshot())
	if !slices.Equal(got, want) {
		t.Errorf("changes differ\npreview:\n  %s\nwritten:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	// Undo first (the flow, in both tools); then writes in the adapters' order: review is new
	// (Other AI reads it from Fake Code's directory), unit-testing is overwritten.
	var acts []Action
	for _, it := range p.Changes() {
		acts = append(acts, it.Action)
	}
	if !slices.Equal(acts, []Action{ActionRevert, ActionRevert, ActionCreate, ActionOverwrite}) {
		t.Errorf("order of changes = %v", acts)
	}
}

// A plan is applied only to the manifest it was made from.
func TestApplyRefusesAStalePlan(t *testing.T) {
	w, r := synced(t)
	st := r.skill("unit-testing", "v2\n").state(t, desired.Filter{})
	p := w.plan(st, Options{})
	w.run(r.without("unit-testing").state(t, desired.Filter{}), Options{}, ConsentInput{})
	s, err := w.eng.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release()
	if _, err := s.Apply(ctx, p, p.Approve(ConsentInput{}, fixedNow())); !errors.Is(err, ErrStalePlan) {
		t.Errorf("Apply of a stale plan: %v", err)
	}
}

// A manifest that cannot be read stops everything: it is never an empty one.
func TestAnUnreadableManifestIsUnknown(t *testing.T) {
	w, r := synced(t)
	w.write("~/"+brand.Home+"/"+home.ManifestFile, "{ not json")
	st := r.state(t, desired.Filter{})
	if _, err := w.eng.Plan(ctx, st, Options{}); !errors.Is(err, manifest.ErrUnknown) {
		t.Errorf("Plan: %v", err)
	}
	if _, err := w.eng.PlanUninstall(ctx, UninstallOptions{}); !errors.Is(err, manifest.ErrUnknown) {
		t.Errorf("PlanUninstall: %v", err)
	}
	if _, err := w.eng.Status(ctx, st, Options{}); !errors.Is(err, manifest.ErrUnknown) {
		t.Errorf("Status: %v", err)
	}
}

// A second program waits, then gives up with "locked".
func TestLockIsExclusive(t *testing.T) {
	w := newWorld(t)
	s, err := w.eng.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release()
	other := w.engine()
	other.wait = 100 * time.Millisecond
	if _, err := other.Lock(ctx); !errors.Is(err, manifest.ErrLocked) {
		t.Errorf("a second Lock: %v", err)
	}
}

// A write that a crash left unrecorded is recorded when the next run takes the lock.
func TestLockRecoversWhatACrashLeft(t *testing.T) {
	w := newWorld(t)
	files := []adapter.File{{Path: "SKILL.md", Data: []byte("---\nname: unit-testing\n---\n")}}
	digest, _ := executor.Expect(adapter.WriteTree{Files: files})
	l, err := manifest.Open(w.data).Lock(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	en := manifest.Entry{ID: unitID, Tool: "fakecode", Resource: "unit-testing", Op: manifest.OpTree, Path: unitDir, Digest: digest, At: fixedNow()}
	if _, err := l.Intend(manifest.Intent{Kind: manifest.Write, Entry: en, After: digest}); err != nil {
		t.Fatal(err)
	}
	w.write(unitSkill, string(files[0].Data)) // the write happened; the process died before Commit
	l.Release()

	s, err := w.eng.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release()
	if len(s.Recovery.Committed) != 1 {
		t.Errorf("recovery = %+v", s.Recovery)
	}
	if _, ok := w.manifest().Find(unitID); !ok {
		t.Error("the interrupted write was not recorded")
	}
}

// Uninstall undoes every entry, newest first, and leaves the home directory as it was before the
// first sync, byte for byte; things with the prefix that are not in the manifest stay.
func TestUninstallRestoresTheHomeDirectory(t *testing.T) {
	w := newWorld(t, fakeCode(), otherAI())
	initial := w.snapshot()
	r := flowRepo()
	r["servers/jira/server.txt"] = "jira-mcp\n"
	r["servers/jira/meta.yaml"] = "name: jira\n"
	w.run(r.state(t, desired.Filter{}), Options{}, ConsentInput{YesGlobal: true})
	if len(w.manifest().Entries) != 4 {
		t.Fatalf("synced %d entries, want 4", len(w.manifest().Entries))
	}

	// --tool: only that tool's entries.
	p, err := w.eng.PlanUninstall(ctx, UninstallOptions{Tools: []string{"otherai"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].Tool != "otherai" {
		t.Errorf("uninstall --tool otherai: %+v", p.Items)
	}

	p, err = w.eng.PlanUninstall(ctx, UninstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m := w.manifest()
	for i, it := range p.Items {
		if it.Action != ActionRevert || it.ID != m.Entries[len(m.Entries)-1-i].ID {
			t.Errorf("item %d: %s %s; uninstall goes newest first", i, it.Action, it.ID)
		}
	}
	if it := item(t, p, "fakecode", unitDir); !slices.Equal(it.SharedWith, []string{"otherai"}) {
		t.Errorf("the uninstall does not say Other AI reads unit-testing too: %v", it.SharedWith)
	}
	s, err := w.eng.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w.log.reset()
	prev, err := w.eng.Preview(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	before := w.snapshot()
	if _, err := s.Apply(ctx, p, p.Approve(ConsentInput{}, fixedNow())); err != nil {
		t.Fatal(err)
	}
	s.Release()
	if got, want := previewChanges(prev), changes(before, w.snapshot()); !slices.Equal(got, want) {
		t.Errorf("uninstall preview and write differ\npreview:\n  %s\nwritten:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if d := changes(initial, w.snapshot()); len(d) > 0 {
		t.Errorf("after uninstalling, the home directory is not as it was: %v", d)
	}
	if len(w.manifest().Entries) != 0 {
		t.Errorf("entries left: %+v", w.manifest().Entries)
	}
}

// A key whose value changes is undone and set again, not replaced in place: replacing records no
// edit, and uninstall could then only remove the key by its meaning instead of restoring the
// file byte for byte.
func TestAnUpdatedKeyStillUninstallsByteForByte(t *testing.T) {
	w := newWorld(t)
	initial := w.read("~/.fakecode/settings.jsonc")
	r := newRepo()
	r["servers/jira/server.txt"] = "jira-mcp\n"
	r["servers/jira/meta.yaml"] = "name: jira\n"
	w.run(r.state(t, desired.Filter{}), Options{}, ConsentInput{YesGlobal: true})
	first, ok := entryByPath(w, "~/.fakecode/settings.jsonc")
	if !ok || first.Edit == nil {
		t.Fatalf("the first write recorded no edit: %+v", first)
	}

	r["servers/jira/server.txt"] = "jira-mcp --verbose\n"
	p, _ := w.run(r.state(t, desired.Filter{}), Options{}, ConsentInput{})
	if it := item(t, p, "fakecode", "~/.fakecode/settings.jsonc"); it.Action != ActionUpdate {
		t.Fatalf("planned %s, want %s", it.Action, ActionUpdate)
	}
	if !strings.Contains(w.read("~/.fakecode/settings.jsonc"), "--verbose") {
		t.Fatal("the key was not updated")
	}
	second, _ := entryByPath(w, "~/.fakecode/settings.jsonc")
	if second.Edit == nil || second.Digest == first.Digest {
		t.Errorf("the update recorded no edit of its own: %+v", second)
	}

	s, err := w.eng.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	up, err := w.eng.PlanUninstall(ctx, UninstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, up, up.Approve(ConsentInput{}, fixedNow())); err != nil {
		t.Fatal(err)
	}
	s.Release()
	if got := w.read("~/.fakecode/settings.jsonc"); got != initial {
		t.Errorf("after update and uninstall the file is not as it was\nwant:\n%s\ngot:\n%s", initial, got)
	}
}

func entryByPath(w *world, path string) (manifest.Entry, bool) {
	for _, en := range w.manifest().Entries {
		if en.Path == path && en.Op == manifest.OpKey {
			return en, true
		}
	}
	return manifest.Entry{}, false
}

// Status: synced, outdated, modified, deleted, unreadable, to be removed, still to come.
func TestStatus(t *testing.T) {
	w := newWorld(t)
	r := newRepo().skill("a-same", "v1\n").skill("b-newer", "v1\n").skill("c-edited", "v1\n").skill("d-deleted", "v1\n").skill("e-gone", "v1\n")
	w.run(r.state(t, desired.Filter{}), Options{}, ConsentInput{})
	dir := func(code string) string { return "~/.fakecode/skills/" + brand.Prefix + code }
	r.skill("b-newer", "v2\n").without("e-gone").skill("f-new", "v1\n")
	w.write(dir("c-edited")+"/SKILL.md", "edited\n")
	if err := os.RemoveAll(w.path(dir("d-deleted"))); err != nil {
		t.Fatal(err)
	}

	st, err := w.eng.Status(ctx, r.state(t, desired.Filter{}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]EntryState{}
	for _, e := range st.Entries {
		got[e.Entry.Resource] = e.State
	}
	want := map[string]EntryState{"a-same": Synced, "b-newer": Outdated, "c-edited": Modified, "d-deleted": Deleted, "e-gone": Removing}
	for code, s := range want {
		if got[code] != s {
			t.Errorf("%s: %s, want %s", code, got[code], s)
		}
	}
	if len(st.Pending) != 1 || st.Pending[0].Resource.Code != "f-new" || !st.Drift() || !st.Differs() {
		t.Errorf("pending %v, drift %v", st.Pending, st.Drift())
	}
	for _, e := range st.Entries {
		if e.Entry.Resource == "b-newer" && e.Want == nil {
			t.Error("an outdated entry carries nothing to diff against")
		}
	}

	// Without a desired state: the disk against the manifest only.
	st, err = w.eng.Status(ctx, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]EntryState{}
	for _, e := range st.Entries {
		got[e.Entry.Resource] = e.State
	}
	want = map[string]EntryState{"a-same": Synced, "b-newer": Synced, "c-edited": Modified, "d-deleted": Deleted, "e-gone": Synced}
	for code, s := range want {
		if got[code] != s {
			t.Errorf("without a source, %s: %s, want %s", code, got[code], s)
		}
	}

	// A target that cannot be read is unreadable, not anything else.
	unixOnly(t)
	ref := w.path(dir("a-same"))
	if err := os.Chmod(ref, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(ref, 0o755)
	st, err = w.eng.Status(ctx, r.state(t, desired.Filter{}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range st.Entries {
		if e.Entry.Resource == "a-same" && e.State != Unreadable {
			t.Errorf("an unreadable entry is %s", e.State)
		}
	}
}
