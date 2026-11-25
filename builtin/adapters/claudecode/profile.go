// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package claudecode

import _ "embed"

// profileYAML is the declarative part of the adapter. The adapter itself (New, and the code for
// what the profile cannot say) is not written yet; until it is, the profile is only checked by
// this package's tests.
//
//go:embed profile.yaml
var profileYAML []byte
