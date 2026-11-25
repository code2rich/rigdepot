// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build !darwin && !windows

package i18n

import "os"

func systemLanguages() []string { return envLanguages(os.Getenv) }
