// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package personalpath

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

func TestLines(t *testing.T) {
	cases := map[string]string{
		"cd /Users/alice/work/shop":              "home_dir",
		"JAVA8_HOME=/Users/alice/jdk8":           "home_dir",
		"export PATH=$PATH:/home/bob/.local/bin": "home_dir",
		`C:\Users\carol\AppData\Local\example`:   "home_dir",
		"c:/Users/carol/projects":                "home_dir",
		"打开 http://localhost:8080/swagger 看接口":   "local_port",
		"url: http://127.0.0.1:3000/mcp":         "local_port",
		// Close, and fine.
		"cd ~/work/shop":        "",
		"JAVA8_HOME=$HOME/jdk8": "",
		"/Users/<你的用户名>/work 换成你自己的目录":                  "",
		"/Users/Shared/example-tool":                    "",
		`C:\Users\Public\Documents`:                     "",
		"/home/user/app":                                "",
		"https://api.example.com/Users/42/profile":      "",
		"GET /api/home/banner":                          "",
		"https://gitlab.example.com:8443/platform/shop": "",
		"服务默认监听 localhost，端口在 .env.example 里配置":         "",
	}
	for line, want := range cases {
		found, err := New().Check(context.Background(), check.Input{
			Ref:   resource.Ref{Kind: resource.KindSkill, Code: "demo"},
			Files: fstest.MapFS{"SKILL.md": {Data: []byte(line + "\n")}},
		})
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case want == "" && len(found) != 0:
			t.Errorf("%q: findings %+v, want none", line, found)
		case want != "" && (len(found) != 1 || found[0].Message != "check.personalpath."+want || found[0].Severity != check.Advisory):
			t.Errorf("%q: findings %+v, want one %s", line, found, want)
		}
	}
}

// The account name is personal information: findings do not carry it.
func TestFindingsDoNotNameTheAccount(t *testing.T) {
	found, err := New().Check(context.Background(), check.Input{
		Ref:   resource.Ref{Kind: resource.KindSkill, Code: "demo"},
		Files: fstest.MapFS{"SKILL.md": {Data: []byte("cd /Users/alice/work\n")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		for _, v := range f.Params {
			if s, ok := v.(string); ok && s == "alice" {
				t.Errorf("finding %+v names the account", f)
			}
		}
	}
}
