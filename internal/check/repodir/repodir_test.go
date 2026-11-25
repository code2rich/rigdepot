// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package repodir

import (
	"errors"
	"io/fs"
	"os"
	"reflect"
	"testing"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/check"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func TestLoad(t *testing.T) {
	got, err := Load(os.DirFS("testdata/repo"))
	if err != nil {
		t.Fatal(err)
	}
	want := []check.Resource{
		{Ref: resource.Ref{Kind: resource.KindMCP, Code: "bad"}, Files: map[string][]byte{"bad.yaml": []byte("not: [yaml\n")}},
		{
			Ref:   resource.Ref{Kind: resource.KindMCP, Code: "jira"},
			Meta:  resource.Meta{Name: "Jira", Summary: "j"},
			Files: map[string][]byte{"jira.yaml": []byte("meta:\n  name: Jira\n  summary: j\nserver:\n  type: http\n")},
		},
		{
			Ref: resource.Ref{Kind: resource.KindSkill, Code: "broken-meta"},
			Files: map[string][]byte{
				"SKILL.md":  []byte("---\nname: broken-meta\n---\n"),
				"meta.yaml": []byte("name: [unclosed\n"),
			},
		},
		{
			Ref:  resource.Ref{Kind: resource.KindSkill, Code: "unit-testing"},
			Meta: resource.Meta{Name: "单元测试", Summary: "s", Stages: []string{"test"}, Policy: resource.PolicyRecommended},
			Files: map[string][]byte{
				"SKILL.md":       []byte("---\nname: unit-testing\ndescription: d\n---\n"),
				"meta.yaml":      []byte("name: 单元测试\nsummary: s\nstages: [test]\npolicy: recommended\nx-team: qa\n"),
				"scripts/run.sh": []byte("echo\n"),
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load =\n%+v\nwant\n%+v", got, want)
	}
}

type failing struct{}

func (failing) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
}

func TestLoadUnreadableIsAnError(t *testing.T) {
	if _, err := Load(failing{}); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("Load on an unreadable directory: %v", err)
	}
}
