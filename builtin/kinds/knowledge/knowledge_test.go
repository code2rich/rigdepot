// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package knowledge

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/kind/kindtest"
	"github.com/code2rich/rigdepot/ext/resource"
)

var update = flag.Bool("update", false, "write testdata/golden from what the kind renders")

func TestMain(m *testing.M) { kindtest.Main(m) }

func TestKind(t *testing.T) { kindtest.Run(t, New(), "testdata") }

var opt = kind.RenderOptions{Prefix: "acme-", CLI: "acme", Data: "~/.acme"}

func parse(t *testing.T, code string) spec {
	t.Helper()
	s, err := New().Parse(resource.Ref{Kind: resource.KindKnowledge, Code: code}, os.DirFS("testdata/valid"))
	if err != nil {
		t.Fatal(err)
	}
	return s.(spec)
}

// The pointer reaches the command line, which fetches with the person's own git.
func TestPull(t *testing.T) {
	if got := parse(t, "eng-handbook").Pull(); got != (kind.GitSource{URL: "https://gitlab.example.com/platform/handbook.git", Ref: "main", Dir: "rules/backend"}) {
		t.Errorf("eng-handbook: %+v", got)
	}
	if got := parse(t, "ops-runbook").Pull(); got != (kind.GitSource{URL: "git@gitlab.example.com:ops/runbook.git"}) {
		t.Errorf("ops-runbook: %+v", got)
	}
}

// The skill says where the documents are on this computer and holds none of them, nor the
// address they came from. The golden file is the review of its text.
func TestSkillGolden(t *testing.T) {
	arts, err := asSkill(parse(t, "eng-handbook"), adapter.Capabilities{Skills: true}, opt)
	if err != nil {
		t.Fatal(err)
	}
	sd := arts[0].(adapter.SkillDir)
	if sd.DirName != "acme-eng-handbook" || len(sd.Files) != 1 || sd.Files[0].Path != "SKILL.md" || sd.Degraded != "" {
		t.Fatalf("artifact %+v", sd)
	}
	data := sd.Files[0].Data
	golden := filepath.Join("testdata", "golden", "eng-handbook.SKILL.md")
	if *update {
		if err := os.WriteFile(golden, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, want) {
		t.Errorf("SKILL.md differs from %s:\n%s", golden, data)
	}
	if !bytes.Contains(data, []byte("~/.acme/knowledge/eng-handbook/")) {
		t.Error("the skill does not say where the documents are")
	}
	if bytes.Contains(data, []byte("gitlab.example.com")) {
		t.Error("the skill holds the repository's address; the AI needs where the documents are on this computer, nothing more")
	}
}

func TestRenderingNeedsTheLocalDataDirectory(t *testing.T) {
	_, err := asSkill(parse(t, "eng-handbook"), adapter.Capabilities{Skills: true}, kind.RenderOptions{Prefix: "acme-", CLI: "acme"})
	if err == nil || errors.Is(err, kind.ErrNotApplicable) {
		t.Errorf("rendering without RenderOptions.Data: %v; want a failure", err)
	}
}
