// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import "testing"

func TestUnifiedDiff(t *testing.T) {
	for name, c := range map[string]struct{ a, b, want string }{
		"one line changed": {
			"a\nb\nc\nd\ne\nf\ng\nh\n", "a\nb\nc\nd\nE\nf\ng\nh\n",
			"@@ -2,7 +2,7 @@\n b\n c\n d\n-e\n+E\n f\n g\n h\n",
		},
		"added at the end without a newline": {
			"a\n", "a\nb",
			"@@ -1,1 +1,2 @@\n a\n+b\n\\ No newline at end of file\n",
		},
		"two hunks": {
			"1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n", "x\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\ny\n",
			"@@ -1,4 +1,4 @@\n-1\n+x\n 2\n 3\n 4\n@@ -9,4 +9,4 @@\n 9\n 10\n 11\n-12\n+y\n",
		},
		"from nothing": {"", "a\n", "@@ -0,0 +1,1 @@\n+a\n"},
		"binary":       {"a\x00", "b\x00", ""},
	} {
		if got := unifiedDiff([]byte(c.a), []byte(c.b)); got != c.want {
			t.Errorf("%s:\n%s\nwant\n%s", name, got, c.want)
		}
	}
}
