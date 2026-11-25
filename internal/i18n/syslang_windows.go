// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package i18n

import "golang.org/x/sys/windows"

func systemLanguages() []string {
	tags, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil {
		return nil
	}
	return tags
}
