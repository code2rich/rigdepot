// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package all

import (
	"github.com/code2rich/rigdepot/builtin/checks/dangerous"
	"github.com/code2rich/rigdepot/builtin/checks/injection"
	"github.com/code2rich/rigdepot/builtin/checks/secrets"
	"github.com/code2rich/rigdepot/builtin/checks/structure"
	"github.com/code2rich/rigdepot/ext/check"
)

// rulesetVersion is the version of the built-in rule set. It is recorded with every check
// result. Raise it by one in the commit that adds or removes a rule or changes what a rule
// reports, once a release has shipped with the current number; published resources are then
// checked again, and what the new version finds is shown as advisory for one period before it
// blocks.
const rulesetVersion = 1

func init() {
	check.SetVersion(rulesetVersion)

	// One line per check rule under builtin/checks, in alphabetical order.
	check.Register(dangerous.New())
	check.Register(injection.New())
	check.Register(secrets.New())
	check.Register(structure.New())
}
