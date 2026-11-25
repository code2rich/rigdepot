// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adaptertest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
)

// suite runs the contract for one adapter.
type suite struct {
	a      adapter.Adapter
	dir    string // the adapter's testdata
	update bool   // rewrite golden/ instead of comparing with it
}

func runSuite(r reporter, a adapter.Adapter, dir string, update bool) {
	r.Helper()
	p := a.Profile()
	if p.ID != a.ID() {
		r.fatal(checkFixtures, "the profile is for %q and the adapter is %q", p.ID, a.ID())
	}
	if p.Verified == nil {
		r.fatal(checkFixtures, "the profile of %q has no verification record, so the adapter delivers nothing and there is no contract to check; verify the tool and record how", a.ID())
	}
	cases, problems := loadCases(dir)
	for _, msg := range problems {
		r.fail(checkFixtures, "%s", msg)
	}
	s := &suite{a: a, dir: dir, update: update}
	for _, c := range cases {
		r.run(c.name, func(r reporter) { s.runCase(r, c) })
	}
}

// manifest is the record of what the core wrote into one home, in the order it was written.
type manifest struct{ entries []entry }

func (m *manifest) find(t target) int {
	for i, e := range m.entries {
		if e.target.id() == t.id() {
			return i
		}
	}
	return -1
}

// syncResult is what one sync did.
type syncResult struct {
	detection  adapter.Detection
	plan       adapter.Plan
	lines      []string // what the core decided, for the golden plan
	writes     int      // operations applied, entries updated or reverted
	unreadable bool     // something could not be read, so the tool was left alone
	drifted    []target // changed or removed by hand: reported, not touched
	taken      []target // the name belongs to the person: reported, not touched
}

// planned runs Detect and Plan against a home.
type planned struct {
	env       toolEnv
	detection adapter.Detection
	plan      adapter.Plan
}

func (s *suite) detectAndPlan(r reporter, home string, c fixtureCase, pure bool) (planned, bool) {
	r.Helper()
	ctx := context.Background()
	env := newToolEnv(home, c.spec, s.a.Profile())
	d, err := s.a.Detect(ctx, env.env)
	if err != nil {
		r.fail(checkDetect, "Detect failed: %v", err)
		return planned{}, false
	}
	if d.Status != adapter.Installed {
		r.fail(checkDetect, "the fixture home is detected as %s (reason %q); every case is a home with the tool installed", d.Status, d.Reason)
		return planned{}, false
	}
	req := adapter.PlanRequest{
		Env:       env.env,
		Detection: d,
		Artifacts: standardArtifacts(s.a.Capabilities(d)),
		Options:   adapter.Options{MCPMode: adapter.MCPNativeEnv},
	}

	var before, realBefore snapshot
	if pure {
		before = mustSnapshot(r, home)
		realBefore = mustSnapshot(r, realHome())
		*env.commands = nil
	}
	plan, err := s.a.Plan(ctx, req)
	if err != nil {
		r.fail(checkCore, "Plan failed: %v", err)
		return planned{}, false
	}
	if pure {
		if cs := diff(before, mustSnapshot(r, home)); len(cs) > 0 {
			r.fail(checkPlanPure, "Plan changed the home directory it was planning for:\n%s", describeChanges(cs))
		}
		if cs := diff(realBefore, mustSnapshot(r, realHome())); len(cs) > 0 {
			r.fail(checkPlanPure, "Plan changed files in the process's home directory:\n%s", describeChanges(cs))
		}
		if len(*env.commands) > 0 {
			r.fail(checkPlanPure, "Plan ran commands: %s", strings.Join(*env.commands, "; "))
		}
		again, err := s.a.Plan(ctx, req)
		if err != nil {
			r.fail(checkDeterministic, "Plan failed the second time: %v", err)
		} else if a, b := describePlan(plan, true), describePlan(again, true); a != b {
			r.fail(checkDeterministic, "the same input gave two different plans:\nfirst:\n%s\nsecond:\n%s", a, b)
		}
	}
	return planned{env: env, detection: d, plan: plan}, true
}

// realHome is the home directory of the test process: the temporary one testenv set up.
func realHome() string {
	h, _ := os.UserHomeDir()
	return h
}

func mustSnapshot(r reporter, root string) snapshot {
	r.Helper()
	s, err := takeSnapshot(root)
	if err != nil {
		r.fatal(checkFixtures, "cannot read %s: %v", root, err)
	}
	return s
}

// apply is the part of the sync engine the contract needs: the table in
// docs/architecture/05-adapters.md ("计划、应用、撤销、漂移"), for one tool.
func (s *suite) apply(r reporter, ex executor, p planned, m *manifest) syncResult {
	r.Helper()
	res := syncResult{detection: p.detection, plan: p.plan}
	for _, sk := range p.plan.Skips {
		if sk.Reason == adapter.ConfigUnreadable {
			res.unreadable = true
			res.lines = append(res.lines, "! tool skipped: "+sk.Detail)
			return res
		}
	}

	type step struct {
		op   adapter.Op
		t    target
		obs  observed
		want string
	}
	var steps []step
	wanted := map[string]bool{}
	for _, op := range p.plan.Ops {
		t := targetOf(op)
		st := step{op: op, t: t, obs: ex.Observe(t)}
		if t.kind != "exec" {
			want, err := ex.Expect(op)
			if err != nil {
				r.fail(checkCore, "%s: %v", t, err)
				continue
			}
			st.want = want
		}
		wanted[t.id()] = true
		steps = append(steps, st)
	}
	// Anything that cannot be read means not knowing: the tool is left alone.
	for _, st := range steps {
		if st.obs.state == unreadable {
			res.unreadable = true
			res.lines = append(res.lines, fmt.Sprintf("! tool skipped: %s cannot be read: %s", st.t, st.obs.reason))
		}
	}
	for _, e := range m.entries {
		if o := ex.Observe(e.target); o.state == unreadable {
			res.unreadable = true
			res.lines = append(res.lines, fmt.Sprintf("! tool skipped: %s cannot be read: %s", e.target, o.reason))
		}
	}
	if res.unreadable {
		return res
	}

	for _, st := range steps {
		if st.t.kind == "exec" {
			res.lines = append(res.lines, "  "+st.t.String()+" (not run in tests)")
			continue
		}
		i := m.find(st.t)
		switch {
		case i < 0 && st.obs.state == absent:
			e, err := ex.Apply(st.op, st.obs)
			if err != nil {
				r.fail(checkCore, "%s: %v", st.t, err)
				continue
			}
			m.entries = append(m.entries, e)
			res.writes++
			res.lines = append(res.lines, "+ "+st.t.String())
		case i < 0:
			res.taken = append(res.taken, st.t)
			res.lines = append(res.lines, "! "+st.t.String()+": "+string(adapter.NameTaken))
		case st.obs.state == absent:
			res.drifted = append(res.drifted, st.t)
			res.lines = append(res.lines, "! "+st.t.String()+": deleted here")
		case st.obs.digest == m.entries[i].digest && st.want == m.entries[i].digest:
			// as wanted
		case st.obs.digest == st.want:
			m.entries[i].digest = st.want // someone made it what we want: only the record changes
		case st.obs.digest == m.entries[i].digest:
			if _, err := ex.Revert(m.entries[i]); err != nil {
				r.fail(checkCore, "update %s: %v", st.t, err)
				continue
			}
			e, err := ex.Apply(st.op, ex.Observe(st.t))
			if err != nil {
				r.fail(checkCore, "update %s: %v", st.t, err)
				continue
			}
			m.entries[i] = e
			res.writes++
			res.lines = append(res.lines, "~ "+st.t.String())
		default:
			res.drifted = append(res.drifted, st.t)
			res.lines = append(res.lines, "! "+st.t.String()+": changed here")
		}
	}
	var keep []entry
	for _, e := range m.entries {
		if wanted[e.target.id()] {
			keep = append(keep, e)
			continue
		}
		if _, err := ex.Revert(e); err != nil {
			r.fail(checkCore, "undo %s: %v", e.target, err)
			keep = append(keep, e)
			continue
		}
		res.writes++
		res.lines = append(res.lines, "- "+e.target.String())
	}
	m.entries = keep
	return res
}

func (s *suite) runCase(r reporter, c fixtureCase) {
	r.Helper()
	stateDir := r.tempDir()
	work := filepath.Join(r.tempDir(), "home")
	if err := copyTree(filepath.Join(c.dir, "home"), work); err != nil {
		r.fatal(checkFixtures, "cannot copy the fixture home: %v", err)
	}
	initial := mustSnapshot(r, work)

	// Plan once against the home, checking that planning is pure.
	first, ok := s.detectAndPlan(r, work, c, true)
	if !ok {
		return
	}
	s.checkTargets(r, first)

	// Preview: the same operations, run in a temporary copy.
	var targets []target
	for _, op := range first.plan.Ops {
		targets = append(targets, targetOf(op))
	}
	preview, err := newSandbox(r.tempDir).Prepare(work, targets)
	if err != nil {
		r.fatal(checkCore, "the sandbox failed: %v", err)
	}
	previewBefore := mustSnapshot(r, preview)
	pp, ok := s.detectAndPlan(r, preview, c, false)
	if !ok {
		return
	}
	s.apply(r, newExecutor(preview, r.tempDir(), declaredPlaces(s.a.Profile(), pp.detection.Roots)), pp, &manifest{})
	previewChanges := diff(previewBefore, mustSnapshot(r, preview))

	// The real sync.
	ex := newExecutor(work, stateDir, declaredPlaces(s.a.Profile(), first.detection.Roots))
	m := &manifest{}
	res := s.apply(r, ex, first, m)
	synced := mustSnapshot(r, work)
	changes := diff(initial, synced)
	if a, b := describeChanges(previewChanges), describeChanges(changes); a != b {
		r.fail(checkPreview, "the preview and the real write differ\npreview:\n%s\nwritten:\n%s", a, b)
	}

	s.checkGolden(r, c, synced, res)
	s.checkOthers(r, initial, synced, m)
	if c.name == caseBroken {
		s.checkUnreadable(r, ex, first, res, changes)
	}
	if c.name == caseOccupied {
		if len(res.taken) == 0 {
			r.fail(checkOccupied, "the case has targets taken by the person's own things, and nothing was reported as %s", adapter.NameTaken)
		}
	}

	// A second sync does nothing.
	if again, ok := s.detectAndPlan(r, work, c, false); ok {
		res2 := s.apply(r, ex, again, m)
		if res2.writes > 0 || len(res2.drifted) > 0 {
			r.fail(checkIdempotent, "a second sync was not empty:\n  %s", strings.Join(res2.lines, "\n  "))
		}
		if cs := diff(synced, mustSnapshot(r, work)); len(cs) > 0 {
			r.fail(checkIdempotent, "a second sync changed files:\n%s", describeChanges(cs))
		}
	}

	s.checkDrift(r, c, ex, m, work)

	// Uninstall: undo every entry, newest first.
	for i := len(m.entries) - 1; i >= 0; i-- {
		if _, err := ex.Revert(m.entries[i]); err != nil {
			r.fail(checkUninstall, "undo %s: %v", m.entries[i].target, err)
		}
	}
	if cs := diff(initial, mustSnapshot(r, work)); len(cs) > 0 {
		r.fail(checkUninstall, "after uninstalling, the home is not as it was before the first sync:\n%s", describeChanges(cs))
	}
}

// declaredPlaces returns the directories and files an adapter may write into: its roots, and the
// targets its profile names.
func declaredPlaces(p adapter.Profile, roots map[string]string) []string {
	places := make([]string, 0, len(roots)+4)
	for _, r := range roots {
		places = append(places, r)
	}
	add := func(tmpl string) {
		if v, err := adapter.ExpandPath(tmpl, roots); err == nil {
			places = append(places, v)
		}
	}
	if p.Skills != nil {
		add(p.Skills.Dir)
	}
	if p.Commands != nil {
		add(p.Commands.Dir)
	}
	if p.Agents != nil {
		add(p.Agents.Dir)
	}
	if p.MCP != nil {
		for _, f := range p.MCP.Files {
			add(f)
		}
	}
	return places
}

func inside(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// checkTargets: every target carries the prefix and lies in a place the adapter declared.
func (s *suite) checkTargets(r reporter, p planned) {
	r.Helper()
	profile := s.a.Profile()
	places := declaredPlaces(profile, p.detection.Roots)
	for _, op := range p.plan.Ops {
		t := targetOf(op)
		if err := checkOwned(op); err != nil {
			r.fail(checkTargets, "%s: %v", t, err)
		}
		if t.kind == "exec" {
			argv := op.(adapter.Exec).Argv
			if len(argv) == 0 || !slices.Contains(profile.Detect.Bins, argv[0]) {
				r.fail(checkTargets, "%s: a command has to be one of the tool's own executables %v", t, profile.Detect.Bins)
			}
			continue
		}
		clean := path.Clean(t.path)
		if clean != t.path || strings.Contains(t.path, "\\") {
			r.fail(checkTargets, "%s: the path is not written in the package form (forward slashes, no \"..\")", t)
		}
		ok := false
		for _, place := range places {
			ok = ok || inside(clean, place)
		}
		if !ok {
			r.fail(checkTargets, "%s lies outside every place the adapter declares (%s)", t, strings.Join(places, ", "))
		}
	}
}

// checkGolden compares the home after the sync, and the plan, with golden/<case>/ and
// golden/<case>.plan; with -update it writes them instead.
func (s *suite) checkGolden(r reporter, c fixtureCase, synced snapshot, res syncResult) {
	r.Helper()
	planText := describePlan(res.plan, false)
	if len(res.lines) > 0 {
		planText += "core:\n  " + strings.Join(res.lines, "\n  ") + "\n"
	}
	treeDir := filepath.Join(s.dir, "golden", c.name)
	planFile := filepath.Join(s.dir, "golden", c.name+".plan")
	if s.update {
		if err := writeSnapshot(treeDir, synced.files()); err != nil {
			r.fatal(checkGolden, "cannot write %s: %v", treeDir, err)
		}
		if err := os.WriteFile(planFile, []byte(planText), 0o644); err != nil {
			r.fatal(checkGolden, "cannot write %s: %v", planFile, err)
		}
		r.Logf("wrote %s and %s; read every file before committing it", treeDir, planFile)
		return
	}
	if _, err := os.Stat(treeDir); err != nil {
		r.fail(checkGolden, "%s is missing; run the tests with -update to write it, then read every file", treeDir)
		return
	}
	golden := mustSnapshot(r, treeDir).files()
	if cs := diff(golden, synced.files()); len(cs) > 0 {
		r.fail(checkGolden, "the home after the sync differs from %s (run with -update and read the diff):\n%s", treeDir, describeChanges(cs))
	}
	want, err := os.ReadFile(planFile)
	if err != nil {
		r.fail(checkGolden, "%s is missing; run the tests with -update to write it", planFile)
		return
	}
	if string(want) != planText {
		r.fail(checkGolden, "the plan differs from %s\nwant:\n%s\ngot:\n%s", planFile, want, planText)
	}
}

// checkOthers: what was there before the sync is still there, byte for byte. A configuration
// file we added keys to may differ by exactly those keys and nothing else.
func (s *suite) checkOthers(r reporter, initial, synced snapshot, m *manifest) {
	r.Helper()
	keysIn := map[string][]entry{} // relative path -> key entries written into it
	for _, e := range m.entries {
		if e.target.kind == "key" && strings.HasPrefix(e.target.path, "~/") {
			keysIn[e.target.path[2:]] = append(keysIn[e.target.path[2:]], e)
		}
	}
	for p, before := range initial {
		after, ok := synced[p]
		switch {
		case !ok:
			r.fail(checkOthers, "%s was there before the sync and is gone", p)
		case before.dir || string(before.data) == string(after.data):
		case len(keysIn[p]) == 0:
			r.fail(checkOthers, "%s was changed, and the sync wrote nothing into it", p)
		default:
			s.checkOnlyOurKeys(r, p, before.data, after.data, keysIn[p])
		}
	}
}

func (s *suite) checkOnlyOurKeys(r reporter, p string, before, after []byte, entries []entry) {
	r.Helper()
	format := entries[0].target.format
	if format == adapter.TOML {
		r.Logf("%s: TOML is not parsed by the suite; the executor's own tests check that only our tables were added", p)
		return
	}
	b, err1 := decodeJSON(before, format)
	a, err2 := decodeJSON(after, format)
	if err1 != nil || err2 != nil {
		r.fail(checkOthers, "%s: does not parse before or after the sync", p)
		return
	}
	for _, e := range entries {
		// Remove the outermost level the sync created, so that new parents go too.
		cut := len(e.target.key)
		for i := 1; i <= len(e.target.key); i++ {
			if _, ok := lookup(b, e.target.key[:i]); !ok {
				cut = i
				break
			}
		}
		a = without(a, e.target.key[:cut])
	}
	if !reflect.DeepEqual(a, b) {
		bj, _ := json.MarshalIndent(b, "", "  ")
		aj, _ := json.MarshalIndent(a, "", "  ")
		r.fail(checkOthers, "%s: apart from our keys the content changed\nbefore:\n%s\nafter, without our keys:\n%s", p, bj, aj)
	}
}

// checkUnreadable: for broken-config, the adapter reports the file and plans nothing for it, and
// nothing at all is written.
func (s *suite) checkUnreadable(r reporter, ex executor, p planned, res syncResult, changes []change) {
	r.Helper()
	reported := false
	for _, sk := range p.plan.Skips {
		reported = reported || sk.Reason == adapter.ConfigUnreadable
	}
	if !reported {
		r.fail(checkUnreadable, "the case has a configuration file that does not parse, and the plan does not report %s", adapter.ConfigUnreadable)
	}
	for _, op := range p.plan.Ops {
		if t := targetOf(op); t.kind == "key" && ex.Observe(t).state == unreadable {
			r.fail(checkUnreadable, "the plan sets %s in a file that does not parse: an unreadable file is never treated as an empty one", t)
		}
	}
	if len(changes) > 0 || res.writes > 0 {
		r.fail(checkUnreadable, "with a configuration file that does not parse, the sync wrote:\n%s", describeChanges(changes))
	}
}

// checkDrift changes a file we wrote by hand and syncs again: the change is reported and kept.
func (s *suite) checkDrift(r reporter, c fixtureCase, ex executor, m *manifest, work string) {
	r.Helper()
	var victim *entry
	for i := range m.entries {
		if k := m.entries[i].target.kind; k == "tree" || k == "file" {
			victim = &m.entries[i]
			break
		}
	}
	if victim == nil {
		if len(m.entries) > 0 {
			r.Logf("drift: nothing but configuration keys was written, so there is no file to change by hand")
		}
		return
	}
	file := filepath.Join(work, filepath.FromSlash(strings.TrimPrefix(victim.target.path, "~/")))
	if victim.target.kind == "tree" {
		entries, err := os.ReadDir(file)
		if err != nil {
			r.fail(checkDrift, "cannot read %s: %v", file, err)
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				file = filepath.Join(file, e.Name())
				break
			}
		}
	}
	data, err := os.ReadFile(file)
	if err != nil {
		r.fail(checkDrift, "cannot read %s: %v", file, err)
		return
	}
	edited := append(data, []byte("\n<!-- changed by hand -->\n")...)
	if err := os.WriteFile(file, edited, 0o644); err != nil {
		r.fatal(checkDrift, "cannot change %s: %v", file, err)
	}
	before := mustSnapshot(r, work)
	again, ok := s.detectAndPlan(r, work, c, false)
	if !ok {
		return
	}
	res := s.apply(r, ex, again, m)
	reported := false
	for _, t := range res.drifted {
		reported = reported || t.id() == victim.target.id()
	}
	if !reported {
		r.fail(checkDrift, "%s was changed by hand and the sync did not report it:\n  %s", victim.target, strings.Join(res.lines, "\n  "))
	}
	if cs := diff(before, mustSnapshot(r, work)); len(cs) > 0 {
		r.fail(checkDrift, "the sync after a change by hand wrote something:\n%s", describeChanges(cs))
	}
	if got, _ := os.ReadFile(file); string(got) != string(edited) {
		r.fail(checkDrift, "the change by hand to %s was overwritten", file)
	}
}
