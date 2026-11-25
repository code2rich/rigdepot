// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package all

import (
	"github.com/code2rich/rigdepot/builtin/kinds/skill"
	"github.com/code2rich/rigdepot/ext/kind"
)

func init() {
	// One line per resource kind under builtin/kinds, in alphabetical order.
	kind.Register(skill.New())
}
