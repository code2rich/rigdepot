// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/check/checktest"
	"github.com/code2rich/rigdepot/ext/resource"
)

func TestMain(m *testing.M) { checktest.Main(m) }

func TestRule(t *testing.T) { checktest.Run(t, New(), "testdata") }

// Each credential is found, and the finding does not contain it. What may show is the fixed
// prefix of its format; nothing of the secret part.
func TestFindingsDoNotContainTheSecret(t *testing.T) {
	cases := []struct {
		line, secret, kind string
	}{
		{"export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE", "IOSFODNN7EXAMPLE", "token_like"},
		{"token ghp_EXAMPLE0example0EXAMPLE0example0EXAM", "EXAMPLE0example0EXAMPLE0example0EXAM", "token_like"},
		{"GITLAB_TOKEN=glpat-Ex4mpleEx4mpleEx4mple", "Ex4mpleEx4mpleEx4mple", "token_like"},
		{"SLACK=xoxb-123456789012-Ex4mpleTokenValue", "123456789012-Ex4mpleTokenValue", "token_like"},
		{"OPENAI_API_KEY=sk-proj-Ex4mpleKeyValue0123456789", "Ex4mpleKeyValue0123456789", "token_like"},
		{"key=AIzaSyEx4mpleEx4mpleEx4mpleEx4mpleEx4mp", "SyEx4mpleEx4mpleEx4mpleEx4mpleEx4mp", "token_like"},
		{"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJleGFtcGxlIn0.c2lnbmF0dXJlRXhhbXBsZQ", "hbGciOiJIUzI1NiJ9.eyJzdWIiOiJleGFtcGxlIn0.c2lnbmF0dXJlRXhhbXBsZQ", "token_like"},
		{"DB_PASSWORD=Sh0pTestExample2026", "Sh0pTestExample2026", "assignment"},
		{`"client_secret": "Ex4mple-Cl1ent-S3cret"`, "Ex4mple-Cl1ent-S3cret", "assignment"},
		{"Authorization: Bearer 8fK2mQ9xLp4ZrT7vW1nY3bHc6dJ0sGeA5uRk", "8fK2mQ9xLp4ZrT7vW1nY3bHc6dJ0sGeA5uRk", "high_entropy"},
	}
	for _, c := range cases {
		t.Run(c.kind+"/"+c.secret[:6], func(t *testing.T) {
			found, err := New().Check(context.Background(), check.Input{
				Ref:   resource.Ref{Kind: resource.KindSkill, Code: "demo"},
				Files: fstest.MapFS{"notes.md": {Data: []byte("intro\n" + c.line + "\n")}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(found) != 1 || found[0].Line != 2 || found[0].File != "notes.md" || found[0].Message != "check.secrets."+c.kind {
				t.Fatalf("findings = %+v, want one %s on line 2", found, c.kind)
			}
			js, err := json.Marshal(found)
			if err != nil {
				t.Fatal(err)
			}
			shown := string(js) + fmt.Sprintf("%+v %#v", found, found)
			for i := 0; i+6 <= len(c.secret); i++ {
				if part := c.secret[i : i+6]; strings.Contains(shown, part) {
					t.Errorf("the finding shows %q, part of the secret: %s", part, js)
					break
				}
			}
		})
	}
}
