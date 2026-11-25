// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package claudecode

import _ "embed"

// profileYAML is the declarative part of the adapter; New reads it.
//
//go:embed profile.yaml
var profileYAML []byte
