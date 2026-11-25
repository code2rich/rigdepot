// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package check

import (
	"regexp"
	"testing"

	extcheck "github.com/code2rich/rigdepot/ext/check"
)

func idOf(files map[string][]byte, f extcheck.Finding) string {
	return FindingIDs(Resource{Files: files}, []extcheck.Finding{f})[0]
}

// A waiver refers to a finding by its identifier: it holds while the content is the same and
// lapses as soon as it changes.
func TestFindingIDFollowsTheContent(t *testing.T) {
	onLine := extcheck.Finding{Rule: "secrets", Message: "check.secrets.assignment", File: "SKILL.md", Line: 2}
	base := map[string][]byte{"SKILL.md": []byte("# a\npassword=hunter22\nend\n")}
	id := idOf(base, onLine)
	if !regexp.MustCompile(`^f-secrets-[0-9a-f]{12}$`).MatchString(id) {
		t.Errorf("identifier %q", id)
	}

	moved := map[string][]byte{"SKILL.md": []byte("# a\n\nmore\npassword=hunter22\n")}
	at4 := onLine
	at4.Line = 4
	if got := idOf(moved, at4); got != id {
		t.Errorf("the line moved and the identifier changed: %s, then %s", id, got)
	}
	crlf := map[string][]byte{"SKILL.md": []byte("# a\r\npassword=hunter22\r\nend\r\n")}
	if got := idOf(crlf, onLine); got != id {
		t.Errorf("a CRLF checkout changed the identifier: %s, then %s", id, got)
	}

	changed := map[string][]byte{"SKILL.md": []byte("# a\npassword=hunter23\nend\n")}
	if got := idOf(changed, onLine); got == id {
		t.Error("the line changed and the identifier stayed")
	}
	other := onLine
	other.Message = "check.secrets.high_entropy"
	if got := idOf(base, other); got == id {
		t.Error("another problem on the same line has the same identifier")
	}

	wholeFile := extcheck.Finding{Rule: "bundledenv", Message: "check.bundledenv.found", File: ".env"}
	a := idOf(map[string][]byte{".env": []byte("A=1\n")}, wholeFile)
	if b := idOf(map[string][]byte{".env": []byte("A=2\n")}, wholeFile); a == b {
		t.Error("the file changed and a finding about the whole file kept its identifier")
	}

	noFile := extcheck.Finding{Rule: "structure", Message: "check.structure.too_many_files", Params: map[string]any{"count": 201, "limit": 200}}
	c := idOf(nil, noFile)
	noFile.Params = map[string]any{"count": 202, "limit": 200}
	if idOf(nil, noFile) == c {
		t.Error("a finding about no file ignores its parameters")
	}
}

func TestFindingIDsAreUniqueInAResult(t *testing.T) {
	files := map[string][]byte{"run.sh": []byte("chmod 777 a\nchmod 777 a\n")}
	f := extcheck.Finding{Rule: "dangerous", Message: "check.dangerous.world_writable", File: "run.sh", Line: 1}
	g := f
	g.Line = 2
	ids := FindingIDs(Resource{Files: files}, []extcheck.Finding{f, g})
	if ids[0] == ids[1] || ids[1] != ids[0]+"-2" {
		t.Errorf("identifiers %v", ids)
	}
}
