// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package all

import (
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/module"
	"github.com/code2rich/rigdepot/ext/module/moduletest"
)

func TestMain(m *testing.M) { moduletest.Main(m) }

// TestRegisteredModulesDescribeThemselves: every module compiled in has the message keys the
// settings page shows before it is turned on.
func TestRegisteredModulesDescribeThemselves(t *testing.T) {
	for _, m := range module.All() {
		info := m.Describe()
		prefix := "module." + m.ID() + "."
		for field, key := range map[string]string{"Name": info.Name, "Description": info.Description} {
			if !strings.HasPrefix(key, prefix) {
				t.Errorf("module %s: Info.%s is %q, want a message key starting with %q", m.ID(), field, key, prefix)
			}
		}
	}
}
