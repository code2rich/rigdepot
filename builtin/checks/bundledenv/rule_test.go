// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package bundledenv

import (
	"testing"

	"github.com/code2rich/rigdepot/ext/check/checktest"
)

func TestMain(m *testing.M) { checktest.Main(m) }

func TestRule(t *testing.T) { checktest.Run(t, New(), "testdata") }

func TestIsEnvFile(t *testing.T) {
	for name, want := range map[string]bool{
		".env":            true,
		".env.local":      true,
		".env.production": true,
		".env.example":    false,
		".env.Example":    false,
		".env.sample":     false,
		".env.template":   false,
		".env.dist":       false,
		".envrc":          false,
		"env":             false,
		"example.env":     false,
		".env.":           false,
	} {
		if got := isEnvFile(name); got != want {
			t.Errorf("isEnvFile(%q) = %v, want %v", name, got, want)
		}
	}
}
