// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package all

import (
	"github.com/code2rich/rigdepot/builtin/kinds/mcp"
	"github.com/code2rich/rigdepot/builtin/kinds/skill"
	"github.com/code2rich/rigdepot/builtin/kinds/workflow"
	"github.com/code2rich/rigdepot/ext/kind"
)

func init() {
	// One line per resource kind under builtin/kinds, in alphabetical order.
	kind.Register(mcp.New())
	kind.Register(skill.New())
	kind.Register(workflow.New())
}
