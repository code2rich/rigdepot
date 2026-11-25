// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/config"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/migrations"
)

func TestResetAdmin(t *testing.T) {
	s := newOpsServer(t, nil)
	s.createAdmin("admin")
	code, stdout, stderr := s.run("reset-admin")
	if code != exitOK {
		t.Fatalf("reset-admin: %d %s", code, stderr)
	}
	m := regexp.MustCompile(`New password for admin: (\S+)`).FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("no password in the output:\n%s", stdout)
	}
	db, svc, log := s.open()
	ctx := context.Background()
	if _, err := svc.Login(ctx, "admin", m[1]); err != nil {
		t.Errorf("the new password: %v", err)
	}
	if _, err := svc.Login(ctx, "admin", opsAdminPassword); err == nil {
		t.Error("the old password still works")
	}
	page, err := log.Query(ctx, audit.Filter{Action: authn.ActionPasswordReset}, 10, "")
	if err != nil || len(page.Rows) != 1 || strings.Contains(string(page.Rows[0].Change), m[1]) {
		t.Errorf("audit: %+v %v", page.Rows, err)
	}
	db.Close()

	// From standard input, for a chosen administrator.
	e, out, errOut := s.env()
	e.stdin = strings.NewReader("<placeholder: chosen password>\n")
	if code := runBounded(e, "reset-admin", "--user", "admin", "--password-stdin"); code != exitOK || strings.Contains(out.String(), "chosen") {
		t.Errorf("--password-stdin: %d %s %s", code, out, errOut)
	}
	db, svc, _ = s.open()
	if _, err := svc.Login(ctx, "admin", "<placeholder: chosen password>"); err != nil {
		t.Errorf("the chosen password: %v", err)
	}
	db.Close()
	if code, _, stderr := s.run("audit", "verify"); code != exitOK {
		t.Errorf("audit verify: %s", stderr)
	}
}

func TestResetAdminFailures(t *testing.T) {
	fresh := newOpsServer(t, nil) // started, no administrator yet
	if code, _, stderr := fresh.run("reset-admin"); code != exitFailure || !strings.Contains(stderr, "no active platform administrator") {
		t.Errorf("no administrator: %d %s", code, stderr)
	}

	s := newOpsServer(t, nil)
	s.createAdmin("admin")
	s.createAdmin("second")
	e, _, _ := testEnv(t, nil)
	notStarted := e.lookupEnvValue(config.EnvName("DATA_DIR"))
	partlyMigrated(t, notStarted, len(mustMigrations(t)))

	tests := []struct {
		name  string
		s     *opsServer
		vars  []string
		args  []string
		stdin string
		want  string
	}{
		{"several administrators", s, nil, nil, "", "admin, second"},
		{"unknown account", s, nil, []string{"--user", "nobody"}, "", `no account named "nobody"`},
		{"short password", s, nil, []string{"--user", "admin", "--password-stdin"}, "short\n", "not accepted"},
		{"empty input", s, nil, []string{"--user", "admin", "--password-stdin"}, "", "standard input"},
		{"no database", s, []string{config.EnvName("DATA_DIR"), filepath.Join(t.TempDir(), "none")}, nil, "", "there is no database"},
		{"never started", s, []string{config.EnvName("DATA_DIR"), notStarted}, nil, "", "has not started on this database"},
		{"key missing", s, []string{config.EnvName("MASTER_KEY_FILE"), filepath.Join(t.TempDir(), "lost.key")}, nil, "", "restore the key file"},
	}
	for _, tt := range tests {
		e, stdout, stderr := tt.s.env(tt.vars...)
		e.stdin = strings.NewReader(tt.stdin)
		code := runBounded(e, append([]string{"reset-admin"}, tt.args...)...)
		if code != exitFailure || !strings.Contains(stderr.String(), tt.want) || strings.Contains(stdout.String(), "password") {
			t.Errorf("%s: %d %q %q", tt.name, code, stdout, stderr)
		}
	}
	// None of them changed anything: the original passwords work.
	db, svc, _ := s.open()
	defer db.Close()
	for _, name := range []string{"admin", "second"} {
		if _, err := svc.Login(context.Background(), name, opsAdminPassword); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func mustMigrations(t *testing.T) []dbx.Migration {
	t.Helper()
	all, err := dbx.LoadMigrations(migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	return all
}
