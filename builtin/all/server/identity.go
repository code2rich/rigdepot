// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"github.com/code2rich/rigdepot/builtin/identity/gitlab"
	"github.com/code2rich/rigdepot/builtin/identity/oidc"
	"github.com/code2rich/rigdepot/ext/identity"
)

func init() {
	// One line per identity source under builtin/identity, in alphabetical order.
	identity.Register(gitlab.New())
	identity.Register(oidc.New())
}
