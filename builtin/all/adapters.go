// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package all

import (
	"github.com/code2rich/rigdepot/builtin/adapters/claudecode"
	"github.com/code2rich/rigdepot/ext/adapter"
)

func init() {
	// One line per tool adapter under builtin/adapters, in alphabetical order.
	adapter.Register(claudecode.New())
}
