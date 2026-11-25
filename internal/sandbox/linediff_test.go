// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sandbox_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/sandbox"
)

func render(lines []sandbox.Line) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteByte(byte(l.Op))
		b.WriteString(l.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestLineDiff(t *testing.T) {
	cases := []struct {
		name, before, after, want string
	}{
		{"same", "a\nb\n", "a\nb\n", " a\n b\n"},
		{"empty to text", "", "a\n", "+a\n"},
		{"text to empty", "a\n", "", "-a\n"},
		{
			"key added in the middle",
			"{\n  \"mcpServers\": {\n    \"mine\": {}\n  }\n}\n",
			"{\n  \"mcpServers\": {\n    \"mine\": {},\n    \"x\": {}\n  }\n}\n",
			" {\n   \"mcpServers\": {\n-    \"mine\": {}\n+    \"mine\": {},\n+    \"x\": {}\n   }\n }\n",
		},
		{"table appended", "a = 1\n", "a = 1\n\n[t]\nb = 2\n", " a = 1\n+\n+[t]\n+b = 2\n"},
		{"missing final newline", "a\nb", "a\nb\n", " a\n-b\n+b\n"},
		{"crlf", "a\r\nb\r\n", "a\r\nc\r\n", " a\n-b\n+c\n"},
		{"interleaved", "a\nb\nc\nd\n", "a\nx\nc\ny\n", " a\n-b\n+x\n c\n-d\n+y\n"},
	}
	for _, c := range cases {
		lines, ok := sandbox.LineDiff([]byte(c.before), []byte(c.after))
		if !ok {
			t.Errorf("%s: not text", c.name)
			continue
		}
		if got := render(lines); got != c.want {
			t.Errorf("%s:\n%s\nwant:\n%s", c.name, got, c.want)
		}
	}
}

func TestLineDiffKeepsEveryLine(t *testing.T) {
	// Whatever the diff, the kept and removed lines are the old text and the kept and added
	// lines the new one.
	before := strings.Repeat("same\nold\n", 50) + "end\n"
	after := "start\n" + strings.Repeat("same\nnew\n", 50)
	lines, ok := sandbox.LineDiff([]byte(before), []byte(after))
	if !ok {
		t.Fatal("not text")
	}
	var old, cur []string
	for _, l := range lines {
		if l.Op != sandbox.Add {
			old = append(old, l.Text)
		}
		if l.Op != sandbox.Del {
			cur = append(cur, l.Text)
		}
	}
	if !slices.Equal(old, strings.Split(strings.TrimSuffix(before, "\n"), "\n")) {
		t.Error("the old text cannot be read back from the diff")
	}
	if !slices.Equal(cur, strings.Split(strings.TrimSuffix(after, "\n"), "\n")) {
		t.Error("the new text cannot be read back from the diff")
	}
}

func TestLineDiffOfBinary(t *testing.T) {
	if _, ok := sandbox.LineDiff([]byte("a\x00b"), []byte("a\n")); ok {
		t.Error("content with a NUL byte was diffed as text")
	}
	if _, ok := sandbox.LineDiff([]byte("a\n"), []byte{0xff, 0xfe}); ok {
		t.Error("content that is not UTF-8 was diffed as text")
	}
}
