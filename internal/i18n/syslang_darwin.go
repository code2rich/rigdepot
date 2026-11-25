// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package i18n

import (
	"context"
	"os"
	"os/exec"
	"time"
)

func systemLanguages() []string {
	return darwinLanguages(os.Getenv, func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "/usr/bin/defaults", "read", "-g", "AppleLanguages").Output()
	})
}
