// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package description

import (
	"context"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/check/checktest"
	"github.com/code2rich/rigdepot/ext/resource"
)

func TestMain(m *testing.M) { checktest.Main(m) }

func TestRule(t *testing.T) { checktest.Run(t, New(), "testdata") }

func TestDescriptions(t *testing.T) {
	cases := map[string][]string{
		"单元测试": {"too_short", "no_when"},
		"为改动生成并运行单元测试，补齐边界用例，输出覆盖率报告": {"no_when"},
		"要给改动补单元测试时使用":                {"too_short"},
		"要给改动补单元测试、补齐边界用例时使用这个技能":     nil,
		"Unit tests":                             {"too_short", "no_when"},
		"Generates unit tests for a change.":     {"no_when"},
		"Use when a change needs unit tests":     nil,
		"Use this to add unit tests to a change": nil,
		// Exactly the minimum, counted in characters, not bytes.
		strings.Repeat("测", 18) + "时用": nil,
		strings.Repeat("测", 17) + "时用": {"too_short"},
	}
	for desc, want := range cases {
		found, err := New().Check(context.Background(), check.Input{
			Ref:   resource.Ref{Kind: resource.KindSkill, Code: "demo"},
			Files: fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: demo\ndescription: " + desc + "\n---\n")}},
		})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range found {
			if f.Line != 3 || f.File != "SKILL.md" {
				t.Errorf("%q: finding %+v is not on the description's line", desc, f)
			}
			got = append(got, strings.TrimPrefix(f.Message, "check.description."))
		}
		if !slices.Equal(got, want) {
			t.Errorf("%q: findings %v, want %v", desc, got, want)
		}
	}
}

// A description that is missing or cannot be read is the structure rule's finding.
func TestNothingToSayWithoutADescription(t *testing.T) {
	for _, text := range []string{"no front matter\n", "---\nname: demo\n---\n", "---\ndescription: [a\n---\n", "---\ndescription: [a, b]\n---\n"} {
		found, err := New().Check(context.Background(), check.Input{
			Ref:   resource.Ref{Kind: resource.KindSkill, Code: "demo"},
			Files: fstest.MapFS{"SKILL.md": {Data: []byte(text)}},
		})
		if err != nil || len(found) != 0 {
			t.Errorf("%q: findings %+v, error %v", text, found, err)
		}
	}
	found, err := New().Check(context.Background(), check.Input{Ref: resource.Ref{Kind: resource.KindSkill, Code: "demo"}, Files: fstest.MapFS{}})
	if err != nil || len(found) != 0 {
		t.Errorf("no SKILL.md: findings %+v, error %v", found, err)
	}
}
