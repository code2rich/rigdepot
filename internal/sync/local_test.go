// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/flow"
	"github.com/code2rich/rigdepot/internal/gitrepo"
)

// pointer is a knowledge base as its kind hands it over: where its content is.
type pointer struct{ src kind.GitSource }

func (p pointer) Pull() kind.GitSource { return p.src }

// handbook is a repository the person can read with their own git. What it holds changes between
// fetches; failing makes the next fetch fail.
type handbook struct {
	files   fstest.MapFS
	commit  string
	failing error
	asked   []gitrepo.Source
}

func (h *handbook) Snapshot(_ context.Context, src gitrepo.Source) (*gitrepo.Snapshot, error) {
	h.asked = append(h.asked, src)
	if h.failing != nil {
		return nil, h.failing
	}
	return &gitrepo.Snapshot{Commit: h.commit, Files: h.files, Links: map[string]string{"latest.md": "rules.md"}}, nil
}

func knowledgeState(codes ...string) *desired.State {
	st := &desired.State{}
	for _, c := range codes {
		st.Resources = append(st.Resources, desired.Resource{
			Ref:  resource.Ref{Kind: resource.KindKnowledge, Code: c},
			Meta: resource.Meta{Name: c},
			Spec: pointer{kind.GitSource{URL: "https://git.example.com/platform/handbook.git", Ref: "main", Dir: "rules/backend"}},
		})
	}
	return st
}

func (w *world) keepLocal(st *desired.State, f Fetcher) *Local {
	w.t.Helper()
	s, err := w.eng.Lock(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	defer s.Release()
	l, err := s.KeepLocal(ctx, st, f)
	if err != nil {
		w.t.Fatal(err)
	}
	return l
}

// A knowledge base is fetched with the person's own git into the local data directory; a new
// fetch replaces the old content as a whole; one that fails keeps what was there.
func TestKnowledgeIsFetchedIntoTheLocalDataDirectory(t *testing.T) {
	w := newWorld(t)
	before := w.snapshot()
	h := &handbook{commit: "c1", files: fstest.MapFS{
		"rules.md":          {Data: []byte("# Rules\n")},
		"api/naming.md":     {Data: []byte("# Naming\n")},
		"CON.md":            {Data: []byte("a name Windows reserves\n")},
		knowledgeMarker:     {Data: []byte("not ours\n")},
		"nested/deep/x.txt": {Data: []byte("x\n")},
	}}
	l := w.keepLocal(knowledgeState("eng-handbook"), h)
	if len(l.Knowledge) != 1 || l.Knowledge[0].Err != nil {
		t.Fatalf("knowledge %+v", l.Knowledge)
	}
	p := l.Knowledge[0]
	if p.Path != "knowledge/eng-handbook" || p.Commit != "c1" || p.Files != 3 || !slices.Equal(p.Skipped, []string{knowledgeMarker, "CON.md", "latest.md"}) {
		t.Errorf("pull %+v", p)
	}
	if got := h.asked; len(got) != 1 || got[0] != (gitrepo.Source{URL: "https://git.example.com/platform/handbook.git", Ref: "main", Dir: "rules/backend"}) {
		t.Errorf("asked for %+v", got)
	}
	dir := w.data.Join("knowledge", "eng-handbook")
	if got, err := os.ReadFile(filepath.Join(dir, "api", "naming.md")); err != nil || string(got) != "# Naming\n" {
		t.Errorf("api/naming.md: %q, %v", got, err)
	}
	if marker, err := os.ReadFile(filepath.Join(dir, knowledgeMarker)); err != nil || !strings.Contains(string(marker), `"commit": "c1"`) {
		t.Errorf("marker: %s, %v", marker, err)
	}
	// Only the local data directory changed: nothing went into a tool.
	for _, c := range changes(before, w.snapshot()) {
		t.Errorf("changed outside the local data directory: %s", c)
	}

	// A new version replaces the content as a whole.
	h.commit, h.files = "c2", fstest.MapFS{"rules.md": {Data: []byte("# Rules, v2\n")}}
	if l := w.keepLocal(knowledgeState("eng-handbook"), h); l.Knowledge[0].Err != nil || l.Knowledge[0].Commit != "c2" {
		t.Fatalf("second fetch %+v", l.Knowledge)
	}
	if _, err := os.Stat(filepath.Join(dir, "api")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a directory of the old version survived: %v", err)
	}

	// The repository cannot be reached: the copy from before stays, and the failure is told.
	h.failing = gitrepo.ErrFetch
	l = w.keepLocal(knowledgeState("eng-handbook"), h)
	if !l.Failed() || !errors.Is(l.Knowledge[0].Err, gitrepo.ErrFetch) {
		t.Fatalf("failing fetch %+v", l.Knowledge)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "rules.md")); err != nil || string(got) != "# Rules, v2\n" {
		t.Errorf("after a failed fetch rules.md is %q, %v", got, err)
	}
	if entries, _ := os.ReadDir(w.data.Join("knowledge")); len(entries) != 1 {
		t.Errorf("left behind in knowledge/: %v", entries)
	}
}

// A knowledge base that is no longer delivered goes; one the repository could not read stays.
func TestKnowledgeNoLongerDeliveredIsRemoved(t *testing.T) {
	w := newWorld(t)
	h := &handbook{commit: "c1", files: fstest.MapFS{"rules.md": {Data: []byte("# Rules\n")}}}
	w.keepLocal(knowledgeState("eng-handbook", "ops-runbook"), h)

	st := knowledgeState()
	st.Failed = []desired.Failed{{Ref: resource.Ref{Kind: resource.KindKnowledge, Code: "ops-runbook"}, Path: "knowledge/ops-runbook.yaml"}}
	l := w.keepLocal(st, h)
	if !slices.Equal(l.KnowledgeRemoved, []string{"eng-handbook"}) {
		t.Errorf("removed %v", l.KnowledgeRemoved)
	}
	if _, err := os.Stat(w.data.Join("knowledge", "ops-runbook", "rules.md")); err != nil {
		t.Errorf("the knowledge base the repository could not read was removed: %v", err)
	}
}

// The workflows are kept for rgd flow next by the same step.
func TestKeepLocalKeepsTheWorkflows(t *testing.T) {
	w := newWorld(t)
	st := &desired.State{Resources: []desired.Resource{{
		Ref: resource.Ref{Kind: resource.KindWorkflow, Code: "ship-it"}, Meta: resource.Meta{Name: "Ship it"}, Digest: "sha256:1",
		Spec: stepsOf{{Use: "ship-it-skill", Then: kind.ThenContinue}},
	}}}
	l := w.keepLocal(st, nil)
	if !slices.Equal(l.Flows.Written, []string{"ship-it"}) {
		t.Fatalf("flows %+v", l.Flows)
	}
	if def, err := flow.Load(w.data, "ship-it"); err != nil || def.Steps[0].Use.Code != "ship-it-skill" {
		t.Errorf("Load: %+v, %v", def, err)
	}
}

type stepsOf []kind.Step

func (s stepsOf) Steps() []kind.Step { return s }

func TestSafeNames(t *testing.T) {
	for name, want := range map[string]bool{
		"rules.md": true, "a/b/c.md": true, "中文/规范.md": true,
		"../x": false, "a\\b": false, "a:b": false, "nul.txt": false, "com1": false, "COM1.md": false,
		"a./b": false, "a /b": false, "tab\tname": false, "com10.md": true,
	} {
		if got := safeName(name); got != want {
			t.Errorf("safeName(%q) = %v, want %v", name, got, want)
		}
	}
}
