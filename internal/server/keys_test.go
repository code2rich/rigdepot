// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/server/config"
)

// TestKeysRotateAndAuditVerifyFailures: the failures of the two commands that came before the
// other operations commands, besides the wrong key and the tampered log (api_test.go).
func TestKeysRotateAndAuditVerifyFailures(t *testing.T) {
	s := newOpsServer(t, nil)
	missing := filepath.Join(t.TempDir(), "none")
	older := filepath.Join(t.TempDir(), "older")
	partlyMigrated(t, older, 5)
	tests := []struct {
		name string
		args []string
		vars []string
		want string
	}{
		{"rotate: no database", []string{"keys", "rotate"}, []string{config.EnvName("DATA_DIR"), missing}, "there is no database"},
		{"verify: no database", []string{"audit", "verify"}, []string{config.EnvName("DATA_DIR"), missing}, "there is no database"},
		{"rotate: not migrated", []string{"keys", "rotate"}, []string{config.EnvName("DATA_DIR"), older}, "run " + brand.Server + " migrate first"},
		{"verify: not migrated", []string{"audit", "verify"}, []string{config.EnvName("DATA_DIR"), older}, "run " + brand.Server + " migrate first"},
		{"rotate: key lost", []string{"keys", "rotate"}, []string{config.EnvName("MASTER_KEY_FILE"), filepath.Join(t.TempDir(), "lost.key")}, "restore the key file"},
		{"verify: key lost", []string{"audit", "verify"}, []string{config.EnvName("MASTER_KEY_FILE"), filepath.Join(t.TempDir(), "lost.key")}, "restore the key file"},
		{"reset-admin: no database", []string{"reset-admin"}, []string{config.EnvName("DATA_DIR"), missing}, "there is no database"},
	}
	for _, tt := range tests {
		e, stdout, stderr := s.env(tt.vars...)
		if code := runBounded(e, tt.args...); code != exitFailure || !strings.Contains(stderr.String(), tt.want) {
			t.Errorf("%s: %d %q %q", tt.name, code, stdout, stderr)
		}
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("a command made the missing data directory")
	}
	// The key given in the environment cannot be rotated in place.
	key, err := os.ReadFile(keyFile(s.dataDir))
	if err != nil {
		t.Fatal(err)
	}
	var current string
	for _, line := range strings.Split(string(key), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			current = line
			break
		}
	}
	e, _, stderr := s.env(config.EnvName("MASTER_KEY"), current, config.EnvName("MASTER_KEY_FILE"), "")
	if code := runBounded(e, "keys", "rotate"); code != exitFailure || !strings.Contains(stderr.String(), "--out") {
		t.Errorf("rotate a key from the environment without --out: %d %s", code, stderr)
	}
}
