// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package api

import _ "embed"

// Spec is the contract, openapi.yaml, as it was when the program was built.
//
//go:embed openapi.yaml
var Spec []byte
