// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package buildinfo

import (
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func TestResolve(t *testing.T) {
	const commit = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
	vcs := map[string]string{"vcs.revision": commit, "vcs.time": "2026-11-25T08:00:00Z"}
	dirty := map[string]string{"vcs.revision": commit, "vcs.modified": "true"}

	tests := []struct {
		name          string
		version       string
		linkedCommit  string
		moduleVersion string
		settings      map[string]string
		want          string
	}{
		{name: "release build", version: "0.4.0", linkedCommit: commit, want: "0.4.0"},
		{name: "release build ignores vcs state", version: "0.4.0", settings: dirty, want: "0.4.0"},
		{name: "go install at a tag", moduleVersion: "v0.4.0", want: "0.4.0"},
		{name: "go build in a clone", moduleVersion: "(devel)", settings: vcs, want: "0.0.0-dev+1a2b3c4d5e6f"},
		{name: "go build with local changes", moduleVersion: "(devel)", settings: dirty, want: "0.0.0-dev+1a2b3c4d5e6f.dirty"},
		{name: "pseudo-version is not a release", moduleVersion: "v0.4.1-0.20261125080000-1a2b3c4d5e6f", settings: vcs, want: "0.0.0-dev+1a2b3c4d5e6f"},
		{name: "untagged work tree", moduleVersion: "v0.0.0-20261125080000-1a2b3c4d5e6f+dirty", settings: dirty, want: "0.0.0-dev+1a2b3c4d5e6f.dirty"},
		{name: "nothing known", want: "0.0.0-dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolve(tt.version, tt.linkedCommit, "", tt.moduleVersion, tt.settings)
			if got.Version != tt.want {
				t.Errorf("version = %q, want %q", got.Version, tt.want)
			}
		})
	}
}

func TestStringNamesTheProgramAndPlatform(t *testing.T) {
	info := resolve("0.4.0", "1a2b3c4d5e6f7a8b", "2026-11-25T08:00:00Z", "", nil)
	got := info.String("prog")
	for _, want := range []string{"prog 0.4.0", "commit 1a2b3c4d5e6f", "2026-11-25T08:00:00Z", info.OS + "/" + info.Arch, info.GoVersion} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "1a2b3c4d5e6f7") {
		t.Errorf("String() = %q, the commit should be shortened", got)
	}
}
