// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package structure

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/check/checktest"
	"github.com/code2rich/rigdepot/ext/resource"
)

func TestMain(m *testing.M) { checktest.Main(m) }

func TestRule(t *testing.T) { checktest.Run(t, New(), "testdata") }

const skillMD = "---\nname: demo\ndescription: Use it when a change needs unit tests\n---\n"

func run(t *testing.T, kind, code string, files fstest.MapFS) []check.Finding {
	t.Helper()
	found, err := New().Check(context.Background(), check.Input{Ref: resource.Ref{Kind: kind, Code: code}, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func messages(found []check.Finding) []string {
	var out []string
	for _, f := range found {
		out = append(out, strings.TrimPrefix(f.Message, "check.structure.")+"@"+f.File)
	}
	return out
}

// The limits are inclusive: exactly at the limit passes, one over is reported.
func TestLimits(t *testing.T) {
	withFiles := func(n int) fstest.MapFS {
		fsys := fstest.MapFS{"SKILL.md": {Data: []byte(skillMD)}}
		for i := 1; i < n; i++ {
			fsys["references/"+strings.Repeat("x", 3)+string(rune('a'+i%26))+strings.Repeat("y", i/26)+".md"] = &fstest.MapFile{Data: []byte("x\n")}
		}
		return fsys
	}
	pathOf := func(n int) string { return "references/" + strings.Repeat("p", n-len("references/")) }
	deep := func(levels int) string { return strings.Repeat("d/", levels-1) + "f.md" }
	big := func(n int) []byte { return make([]byte, n-len(skillMD)) }

	cases := []struct {
		name  string
		files fstest.MapFS
		want  string
	}{
		{"200 files", withFiles(200), ""},
		{"201 files", withFiles(201), "too_many_files@"},
		{"5 MB", fstest.MapFS{"SKILL.md": {Data: []byte(skillMD)}, "big.bin": {Data: big(maxBytes)}}, ""},
		{"5 MB and a byte", fstest.MapFS{"SKILL.md": {Data: []byte(skillMD)}, "big.bin": {Data: big(maxBytes + 1)}}, "too_large@"},
		{"path of 180 bytes", fstest.MapFS{"SKILL.md": {Data: []byte(skillMD)}, pathOf(180): {}}, ""},
		{"path of 181 bytes", fstest.MapFS{"SKILL.md": {Data: []byte(skillMD)}, pathOf(181): {}}, "path_too_long@" + pathOf(181)},
		{"8 levels", fstest.MapFS{"SKILL.md": {Data: []byte(skillMD)}, deep(8): {}}, ""},
		{"9 levels", fstest.MapFS{"SKILL.md": {Data: []byte(skillMD)}, deep(9): {}}, "path_too_deep@" + deep(9)},
		{"names differing in case", fstest.MapFS{"SKILL.md": {Data: []byte(skillMD)}, "README.md": {}, "readme.md": {}}, "case_clash@readme.md"},
		{"directories differing in case", fstest.MapFS{"SKILL.md": {Data: []byte(skillMD)}, "Docs/a.md": {}, "docs/b.md": {}}, "case_clash@"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := messages(run(t, resource.KindSkill, "demo", c.files))
			if c.want == "" && len(got) != 0 || c.want != "" && (len(got) != 1 || got[0] != c.want) {
				t.Errorf("findings = %v, want %q", got, c.want)
			}
		})
	}
}

func TestFindingsPointAtTheLine(t *testing.T) {
	found := run(t, resource.KindSkill, "unit-testing", fstest.MapFS{
		"SKILL.md": {Data: []byte("---\n# a comment\ndescription: Use it when a change needs unit tests\nname: unit-test\n---\n")},
	})
	if len(found) != 1 || found[0].Line != 4 || found[0].File != "SKILL.md" ||
		found[0].Params["name"] != "unit-test" || found[0].Params["code"] != "unit-testing" {
		t.Errorf("findings = %+v", found)
	}

	found = run(t, resource.KindMCP, "gitlab", fstest.MapFS{
		"gitlab.yaml": {Data: []byte("meta:\n  name: GitLab\nserver:\n  type: stdio\n  command: npx\n  args:\n    - -y\n    - ${secret:GITLAB_TOKEN}\n")},
	})
	if len(found) != 1 || found[0].Line != 8 || found[0].Message != "check.structure.secret_not_allowed" || found[0].Params["field"] != "args" {
		t.Errorf("findings = %+v", found)
	}
}

func TestEmptyMCPFile(t *testing.T) {
	found := run(t, resource.KindMCP, "jira", fstest.MapFS{"jira.yaml": {Data: nil}})
	if len(found) != 1 || found[0].Message != "check.structure.yaml_invalid" || found[0].Line != 0 || found[0].File != "jira.yaml" {
		t.Errorf("findings = %+v", found)
	}
}
