// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build rgd_core_only

package all

import (
	"testing"

	"github.com/code2rich/rigdepot/ext/module"
)

// TestCoreOnlyHasNoModules: built with -tags rgd_core_only, the program contains no module.
func TestCoreOnlyHasNoModules(t *testing.T) {
	if mods := module.All(); len(mods) != 0 {
		t.Errorf("%d modules are compiled in under rgd_core_only; every modules/all/maybe_<id>.go needs the constraint !rgd_omit_<id> && !rgd_core_only", len(mods))
	}
}
