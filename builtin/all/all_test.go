// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package all

import (
	"slices"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/check/checktest"
	"github.com/code2rich/rigdepot/ext/kind"
)

func TestMain(m *testing.M) { checktest.Main(m) }

// Importing this package runs the registrations. Two items with the same ID would already have
// stopped the test binary; what is left to check is what the programs read afterwards.
func TestRegistriesLoad(t *testing.T) {
	rs := check.DefaultRuleset()
	if rs.Version != rulesetVersion || rs.Version < 1 {
		t.Errorf("the built-in rule set has version %d, want %d", rs.Version, rulesetVersion)
	}
	var ruleIDs, adapterIDs, kindIDs, kindDirs []string
	for _, r := range rs.Rules {
		ruleIDs = append(ruleIDs, r.ID())
	}
	for _, a := range adapter.All() {
		adapterIDs = append(adapterIDs, a.ID())
		if p := a.Profile(); p.ID != a.ID() {
			t.Errorf("adapter %q has a profile for %q", a.ID(), p.ID)
		}
	}
	for _, k := range kind.All() {
		kindIDs = append(kindIDs, k.ID())
		dir := k.Layout().Dir
		if dir == "" {
			t.Errorf("kind %q has no directory in the resource repository", k.ID())
		}
		if slices.Contains(kindDirs, dir) {
			t.Errorf("kind %q claims the directory %q, which another kind already has", k.ID(), dir)
		}
		kindDirs = append(kindDirs, dir)
	}
	for class, ids := range map[string][]string{"check rules": ruleIDs, "adapters": adapterIDs, "resource kinds": kindIDs} {
		if !slices.IsSorted(ids) {
			t.Errorf("%s are not returned in order: %v", class, ids)
		}
	}
}
