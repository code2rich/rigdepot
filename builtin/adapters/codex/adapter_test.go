// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package codex

import (
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter/adaptertest"
)

func TestContract(t *testing.T) { adaptertest.Run(t, New(), "testdata") }
