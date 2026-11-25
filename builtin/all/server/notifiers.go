// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"github.com/code2rich/rigdepot/builtin/notifiers/email"
	"github.com/code2rich/rigdepot/ext/notify"
)

func init() {
	// One line per notification channel under builtin/notifiers, in alphabetical order.
	notify.Register(email.New())
}
