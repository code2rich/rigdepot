// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package overlap

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/check/checktest"
	"github.com/code2rich/rigdepot/ext/resource"
)

func TestMain(m *testing.M) { checktest.Main(m) }

func TestRule(t *testing.T) { checktest.Run(t, New(), "testdata") }

func TestPairs(t *testing.T) {
	cases := []struct {
		a, b    string
		overlap bool
	}{
		{"要给改动补单元测试时使用，覆盖边界用例", "要给改动补单元测试、覆盖边界用例时使用", true},
		{"Use when a change needs unit tests and edge cases", "Use when a change needs unit tests, including edge cases", true},
		{"要给改动补单元测试时使用，覆盖边界用例", "提交合并请求前自查代码时使用", false},
		{"要给改动补单元测试时使用", "要给改动做压力测试时使用", false},
		{"Use when a change needs unit tests", "Use when a merge request needs a review", false},
	}
	for _, c := range cases {
		found, err := New().Check(context.Background(), check.Input{
			Ref:   resource.Ref{Kind: resource.KindSkill, Code: "one"},
			Files: fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: one\ndescription: " + c.a + "\n---\n")}},
			Peers: check.Peers{
				{Ref: resource.Ref{Kind: resource.KindSkill, Code: "two"}, Description: c.b},
				// Not a skill: an AI tool does not choose between it and a skill by description.
				{Ref: resource.Ref{Kind: resource.KindMCP, Code: "three"}, Description: c.a},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := len(found) == 1; got != c.overlap || len(found) > 1 {
			t.Errorf("%q and %q: findings %+v, want overlap %v (similarity %.2f)", c.a, c.b, found, c.overlap, similarity(terms(c.a), terms(c.b)))
			continue
		}
		if c.overlap && (found[0].Params["peer"] != "two" || found[0].Line != 3) {
			t.Errorf("finding %+v", found[0])
		}
	}
}

func TestNoPeersNothingToSay(t *testing.T) {
	found, err := New().Check(context.Background(), check.Input{
		Ref:   resource.Ref{Kind: resource.KindSkill, Code: "one"},
		Files: fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: one\ndescription: 要给改动补单元测试时使用\n---\n")}},
	})
	if err != nil || len(found) != 0 {
		t.Errorf("findings %+v, error %v", found, err)
	}
}
