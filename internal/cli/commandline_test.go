// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
)

// The command a plan suggests running next must name the same source as the plan: without
// --dir or --branch it would sync something else.
func TestCommandLineKeepsTheSource(t *testing.T) {
	got := commandLine("sync", sourceFlags{repo: ".", branch: "release", dir: "examples/resources", products: []string{"shop", "pay"}})
	want := brand.CLI + " sync --repo . --branch release --dir examples/resources --product shop --product pay"
	if got != want {
		t.Errorf("commandLine = %q, want %q", got, want)
	}
	if got := commandLine("sync", sourceFlags{repo: "my repo"}); got != brand.CLI+` sync --repo "my repo"` {
		t.Errorf("a path with a space: %q", got)
	}
}
