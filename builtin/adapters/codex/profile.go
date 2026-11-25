// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package codex

import _ "embed"

// profileYAML is the declarative part of the adapter; New adds the code for what it cannot say.
//
//go:embed profile.yaml
var profileYAML []byte
