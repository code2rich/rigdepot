// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package resource

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func TestValidCode(t *testing.T) {
	valid := []string{
		"ab",
		"unit-testing",
		"api2",
		"2fa",
		"a-b-c",
		strings.Repeat("a", MaxCodeLength),
	}
	for _, code := range valid {
		if !ValidCode(code) {
			t.Errorf("ValidCode(%q) = false, want true", code)
		}
	}
	invalid := []string{
		"",
		"a", // one character short of the minimum
		strings.Repeat("a", MaxCodeLength+1),
		"Unit-Testing",
		"unit_testing",
		"unit testing",
		"-unit",
		"unit-",
		"unit--testing",
		"unit.testing",
		"skills/unit",
		"单元测试",
		"unit-testing\n",
	}
	for _, code := range invalid {
		if ValidCode(code) {
			t.Errorf("ValidCode(%q) = true, want false", code)
		}
	}
}

func TestValidSecretName(t *testing.T) {
	for _, name := range []string{"JIRA_API_TOKEN", "A1", "GITLAB_TOKEN", "X" + strings.Repeat("Y", 63)} {
		if !ValidSecretName(name) {
			t.Errorf("ValidSecretName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "A", "jira_token", "1TOKEN", "_TOKEN", "JIRA-TOKEN", "JIRA TOKEN", "X" + strings.Repeat("Y", 64)} {
		if ValidSecretName(name) {
			t.Errorf("ValidSecretName(%q) = true, want false", name)
		}
	}
}

func TestRefString(t *testing.T) {
	cases := []struct {
		ref  Ref
		want string
	}{
		{Ref{Kind: KindSkill, Code: "unit-testing", Version: 5}, "skill/unit-testing@5"},
		{Ref{Kind: KindMCP, Code: "jira"}, "mcp/jira"},
	}
	for _, c := range cases {
		if got := c.ref.String(); got != c.want {
			t.Errorf("%#v.String() = %q, want %q", c.ref, got, c.want)
		}
	}
}

// A manifest, a plan and a sync run all key what they know by resource. If Ref stops being
// comparable this no longer compiles.
func TestRefIsAMapKey(t *testing.T) {
	seen := map[Ref]bool{{Kind: KindSkill, Code: "unit-testing", Version: 5}: true}
	if !seen[Ref{Kind: KindSkill, Code: "unit-testing", Version: 5}] {
		t.Error("two equal references are different map keys")
	}
	if seen[Ref{Kind: KindSkill, Code: "unit-testing", Version: 6}] {
		t.Error("references to different versions are the same map key")
	}
}

func TestScopeProduct(t *testing.T) {
	for _, s := range []Scope{"", ScopeOrg, ScopeMe} {
		if code, ok := s.Product(); ok {
			t.Errorf("Scope(%q).Product() = %q, true; it is not a product", s, code)
		}
	}
	if code, ok := ProductScope("shop").Product(); !ok || code != "shop" {
		t.Errorf(`ProductScope("shop").Product() = %q, %v`, code, ok)
	}
}

func TestPolicyAndAudienceValid(t *testing.T) {
	for _, p := range []Policy{PolicyRequired, PolicyRecommended, PolicyOptional} {
		if !p.Valid() {
			t.Errorf("Policy(%q).Valid() = false", p)
		}
	}
	for _, p := range []Policy{"", "mandatory", "Required"} {
		if p.Valid() {
			t.Errorf("Policy(%q).Valid() = true", p)
		}
	}
	for _, a := range []Audience{AudienceRD, AudienceMgmt} {
		if !a.Valid() {
			t.Errorf("Audience(%q).Valid() = false", a)
		}
	}
	for _, a := range []Audience{"", "dev", "RD"} {
		if a.Valid() {
			t.Errorf("Audience(%q).Valid() = true", a)
		}
	}
}

func TestDefaultStages(t *testing.T) {
	want := []string{"requirement", "architecture", "design", "develop", "test", "scm", "release", "ops"}
	got := DefaultStages()
	if len(got) != len(want) {
		t.Fatalf("DefaultStages() has %d stages, want %d", len(got), len(want))
	}
	for i, s := range got {
		if s.ID != want[i] || s.Name != "" || s.Hidden {
			t.Errorf("DefaultStages()[%d] = %+v, want only ID %q", i, s, want[i])
		}
	}
	got[0].ID = "tampered"
	if DefaultStages()[0].ID != "requirement" {
		t.Error("a caller changed its slice and the defaults changed with it")
	}
}

func TestDigestValid(t *testing.T) {
	hex := strings.Repeat("0123456789abcdef", 4)
	if d := Digest("sha256:" + hex); !d.Valid() {
		t.Errorf("Digest(%q).Valid() = false", d)
	}
	for _, d := range []Digest{
		"",
		Digest(hex),
		Digest("sha256:" + hex[:63]),
		Digest("sha256:" + hex + "0"),
		Digest("sha256:" + strings.ToUpper(hex)),
		Digest("sha1:" + hex),
		Digest("sha256:" + hex + "\n"),
	} {
		if d.Valid() {
			t.Errorf("Digest(%q).Valid() = true", d)
		}
	}
}

// The JSON names of Meta are the field names of meta.yaml. This is the sample from the format
// documentation, written the way a YAML reader hands it over.
func TestMetaJSONNamesMatchTheRepositoryFormat(t *testing.T) {
	const doc = `{
		"name": "单元测试",
		"summary": "为改动生成并运行单元测试，补齐边界用例",
		"stages": ["test"],
		"audience": "rd",
		"scope": "org",
		"policy": "recommended",
		"when": "要给改动补单元测试时",
		"try": "给我刚改的这几个类补单元测试",
		"secrets": ["SHOP_TEST_API_TOKEN"],
		"maintainers": ["xuran"],
		"tags": ["java"],
		"i18n": {"en": {"name": "Unit testing", "summary": "Generate and run unit tests", "when": "When a change needs unit tests", "try": "Add unit tests"}}
	}`
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.DisallowUnknownFields()
	var m Meta
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("the documented sample does not decode into Meta: %v", err)
	}
	if m.Name != "单元测试" || m.Audience != AudienceRD || m.Scope != ScopeOrg || m.Policy != PolicyRecommended {
		t.Errorf("decoded Meta = %+v", m)
	}
	if len(m.Stages) != 1 || m.Stages[0] != "test" || len(m.Secrets) != 1 || len(m.Maintainers) != 1 || len(m.Tags) != 1 {
		t.Errorf("decoded lists = %v %v %v %v", m.Stages, m.Secrets, m.Maintainers, m.Tags)
	}
	if en := m.I18n["en"]; en.Name != "Unit testing" || en.Summary == "" || en.When == "" || en.Try == "" {
		t.Errorf(`decoded I18n["en"] = %+v`, en)
	}

	out, err := json.Marshal(Meta{Name: "Jira"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"name":"Jira"}` {
		t.Errorf("a Meta with only a name encodes as %s; fields the resource did not set must stay out", out)
	}
}
