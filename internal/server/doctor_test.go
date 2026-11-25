// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/config"
)

func TestConfigCheck(t *testing.T) {
	e, stdout, stderr := testEnv(t, map[string]string{
		config.EnvName("MASTER_KEY"):   "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
		config.EnvName("DATABASE_URL"): "postgres://app:Pw0rdMarker@db.example.com/rgd",
		config.EnvName("LISTEN"):       "127.0.0.1:8080",
	})
	if code := runBounded(e, "config", "check"); code != exitOK {
		t.Fatalf("config check: %d %s %s", code, stdout, stderr)
	}
	out := stdout.String()
	for _, s := range config.Settings() {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(s.Key) + `\s`).MatchString(out) {
			t.Errorf("%s is not listed", s.Key)
		}
	}
	if strings.Contains(out, "AAECAwQF") || strings.Contains(out, "Pw0rdMarker") || strings.Contains(out, "?") {
		t.Errorf("a secret or an unknown value in the output:\n%s", out)
	}
	for _, want := range []string{"master_key ", "set", "env " + config.EnvName("MASTER_KEY"), "postgres://app:xxxxx@db.example.com/rgd", "No problems found"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// A contradiction: https outside, nothing serving it.
	e, stdout, _ = testEnv(t, map[string]string{config.EnvName("BASE_URL"): "https://hub.example.com"})
	if code := runBounded(e, "config", "check"); code != exitFailure || !strings.Contains(stdout.String(), "problem: base_url is https://") {
		t.Errorf("https without TLS or proxy: %d\n%s", code, stdout)
	}
	// A configuration that does not load at all.
	e, stdout, stderr = testEnv(t, map[string]string{config.EnvName("LISTEN"): "nonsense"})
	if code := runBounded(e, "config", "check"); code != exitUsage || stdout.String() != "" || !strings.Contains(stderr.String(), "listen") {
		t.Errorf("invalid: %d %q %q", code, stdout, stderr)
	}
}

func TestConfigFindings(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string // "" for none
	}{
		{"local trial", map[string]string{config.EnvName("BASE_URL"): "http://127.0.0.1:8080", config.EnvName("LISTEN"): "127.0.0.1:8080"}, ""},
		{"behind a proxy", map[string]string{config.EnvName("LISTEN"): "127.0.0.1:8080", config.EnvName("TRUSTED_PROXIES"): "127.0.0.1", config.EnvName("MASTER_KEY_FILE"): "/etc/rgd/master.key"}, ""},
		{"https, nothing serves it", map[string]string{}, "problem: base_url is https://"},
		{"proxy with http base", map[string]string{config.EnvName("BASE_URL"): "http://127.0.0.1:8080", config.EnvName("TRUSTED_PROXIES"): "127.0.0.1"}, "problem: a reverse proxy is declared"},
		{"proxy, open port", map[string]string{config.EnvName("LISTEN"): "0.0.0.0:8080", config.EnvName("TRUSTED_PROXIES"): "10.0.0.0/8"}, "note: behind a reverse proxy, but listening on 0.0.0.0:8080"},
		{"acme off 443", map[string]string{config.EnvName("TLS_ACME_DOMAINS"): "hub.example.com"}, "note: automatic certificates are checked by the authority on port 443"},
		{"backup with postgres", map[string]string{config.EnvName("TRUSTED_PROXIES"): "127.0.0.1", config.EnvName("BACKUP_DIR"): "/srv/b", config.EnvName("DATABASE_URL"): "postgres://app@db.example.com/x"}, "problem: backup.dir is set, but"},
		{"key in the backups", map[string]string{config.EnvName("TRUSTED_PROXIES"): "127.0.0.1", config.EnvName("BACKUP_DIR"): "/srv/b", config.EnvName("MASTER_KEY_FILE"): "/srv/b/master.key"}, "problem: the master key file is inside backup.dir"},
		{"key in the data directory", map[string]string{config.EnvName("TRUSTED_PROXIES"): "127.0.0.1"}, "note: the master key file is in the data directory"},
	}
	for _, tt := range tests {
		vars := map[string]string{config.EnvName("BASE_URL"): "https://hub.example.com", config.EnvName("LISTEN"): "127.0.0.1:8080"}
		for k, v := range tt.env {
			vars[k] = v
		}
		cfg, err := config.Load(config.Options{LookupEnv: func(k string) (string, bool) { v, ok := vars[k]; return v, ok }})
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		var got []string
		for _, f := range configFindings(cfg) {
			got = append(got, f.level+": "+f.text)
		}
		all := strings.Join(got, "\n")
		if tt.want == "" && strings.Contains(all, "problem") || tt.want != "" && !strings.Contains(all, tt.want) {
			t.Errorf("%s: %s", tt.name, all)
		}
	}
}

// doctorLines runs doctor and returns its exit code and the status of each item by name (the
// worst when an item appears more than once).
func doctorLines(t *testing.T, e *env) (int, map[string]string, string) {
	t.Helper()
	// git in tests is a stand-in that fails, and starting it can take long on some systems:
	// doctor looks for one that is not there.
	lookup := e.lookupEnv
	e.lookupEnv = func(k string) (string, bool) {
		if k == config.EnvName("GIT") {
			return "rgd-test-no-such-git", true
		}
		return lookup(k)
	}
	code := runBounded(e, "doctor")
	out := e.stdout.(*syncBuffer).String()
	status := map[string]string{}
	rank := map[string]int{statusSkip: 0, statusOK: 1, statusWarn: 2, statusFail: 3}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 23 {
			continue
		}
		st, name := strings.TrimSpace(line[:5]), strings.TrimSpace(line[6:22])
		if _, ok := rank[st]; ok && rank[st] >= rank[status[name]] {
			status[name] = st
		}
	}
	return code, status, out + e.stderr.(*syncBuffer).String()
}

func TestDoctorOnAHealthyServer(t *testing.T) {
	s := newOpsServer(t, nil)
	s.createAdmin("admin")
	e, _, _ := s.env(config.EnvName("BACKUP_DIR"), filepath.Join(t.TempDir(), "b"))
	code, status, out := doctorLines(t, e)
	if code != exitOK {
		t.Fatalf("doctor: %d\n%s", code, out)
	}
	want := map[string]string{
		"config": statusOK, "database": statusOK, "migrations": statusOK, "master key": statusOK, "audit log": statusOK,
		"mail": statusWarn, "certificate": statusSkip, "disk space": statusOK, "backups": statusWarn,
	}
	if runtime.GOOS != "windows" {
		want["data directory"] = statusOK
	}
	for name, st := range want {
		if status[name] != st {
			t.Errorf("%s: %q, want %q\n%s", name, status[name], st, out)
		}
	}
	if status["git"] != statusWarn || !strings.Contains(out, "needed to fetch resource repositories") {
		t.Errorf("git: %q\n%s", status["git"], out)
	}
}

// TestDoctorBeforeTheFirstStart: nothing is there yet, which is not a failure, and doctor
// creates nothing.
func TestDoctorBeforeTheFirstStart(t *testing.T) {
	e, _, _ := testEnv(t, nil)
	dataDir := e.lookupEnvValue(config.EnvName("DATA_DIR"))
	code, status, out := doctorLines(t, e)
	if code != exitOK || status["data directory"] != statusWarn || status["database"] != statusWarn || status["master key"] != statusWarn {
		t.Errorf("doctor: %d\n%s", code, out)
	}
	if _, err := os.Stat(dataDir); err == nil {
		t.Error("doctor created the data directory")
	}
}

func TestDoctorFindsProblems(t *testing.T) {
	expired := t.TempDir()
	certFile, certKey := filepath.Join(expired, "cert.pem"), filepath.Join(expired, "key.pem")
	writeCert(t, certFile, certKey, 1, 0)
	// writeCert's certificate lasts 30 days: in 40 it has expired, in 20 it expires soon.

	tests := []struct {
		name   string
		prep   func(s *opsServer)
		vars   []string
		now    time.Duration
		item   string
		status string
		want   string
	}{
		{name: "master key lost", prep: func(s *opsServer) { os.Remove(keyFile(s.dataDir)) }, item: "master key", status: statusFail, want: "restore the key file"},
		{name: "another master key", prep: func(s *opsServer) {
			other := filepath.Join(s.dataDir, "other.key")
			writeKeyFile(other)
			s.vars[config.EnvName("MASTER_KEY_FILE")] = other
		}, item: "master key", status: statusFail, want: "does not open this database"},
		{name: "migrations not run", prep: func(s *opsServer) {
			os.Remove(s.dbPath())
			os.Remove(s.dbPath() + "-wal")
			os.Remove(s.dbPath() + "-shm")
			partlyMigrated(s.t, s.dataDir, 5)
		}, vars: []string{config.EnvName("AUTO_MIGRATE"), "false"}, item: "migrations", status: statusFail, want: "migrate"},
		{name: "newer database", prep: func(s *opsServer) {
			editSQLite(s.t, s.dbPath(), `INSERT INTO schema_migrations (namespace, version, name, checksum, applied_at) VALUES ('core', 9999, '9999_future.sql', 'x', 0)`)
		}, item: "migrations", status: statusFail, want: "newer"},
		{name: "certificate expired", vars: []string{config.EnvName("TLS_CERT_FILE"), certFile, config.EnvName("TLS_KEY_FILE"), certKey,
			config.EnvName("BASE_URL"), "https://hub.example.com"}, now: 40 * 24 * time.Hour, item: "certificate", status: statusFail, want: "expired"},
		{name: "certificate expires soon", vars: []string{config.EnvName("TLS_CERT_FILE"), certFile, config.EnvName("TLS_KEY_FILE"), certKey,
			config.EnvName("BASE_URL"), "https://hub.example.com"}, now: 20 * 24 * time.Hour, item: "certificate", status: statusWarn, want: "expires in"},
		{name: "certificate for another name", vars: []string{config.EnvName("TLS_CERT_FILE"), certFile, config.EnvName("TLS_KEY_FILE"), certKey,
			config.EnvName("BASE_URL"), "https://other.example.com"}, item: "certificate", status: statusWarn, want: "does not cover other.example.com"},
		{name: "certificate files broken", vars: []string{config.EnvName("TLS_CERT_FILE"), certKey, config.EnvName("TLS_KEY_FILE"), certKey,
			config.EnvName("BASE_URL"), "https://hub.example.com"}, item: "certificate", status: statusFail, want: "certificate"},
		{name: "automatic certificate not issued", vars: []string{config.EnvName("TLS_ACME_DOMAINS"), "hub.example.com", config.EnvName("BASE_URL"), "https://hub.example.com"},
			item: "certificate", status: statusWarn, want: "not issued yet"},
		{name: "mail unreachable", prep: func(s *opsServer) {
			editSQLite(s.t, s.dbPath(), `INSERT INTO settings (key, value, encrypted, updated_at) VALUES ('mail', '{"enabled":true,"host":"mail.example.com","port":465,"security":"tls"}', FALSE, 0)`)
		}, item: "mail", status: statusWarn, want: "cannot reach the mail server mail.example.com:465"},
		{name: "config contradiction", vars: []string{config.EnvName("BASE_URL"), "https://hub.example.com"}, item: "config", status: statusFail, want: "base_url is https://"},
		{name: "audit log broken", prep: func(s *opsServer) {
			editSQLite(s.t, s.dbPath(), `INSERT INTO settings (key, value, encrypted, updated_at) VALUES ('audit.verified', '{"last_seq":0,"last_hash":"","at":"2026-11-25T00:00:00Z","broken":"hash mismatch","broken_seq":1}', FALSE, 0) ON CONFLICT (key) DO UPDATE SET value = excluded.value`)
		}, item: "audit log", status: statusFail, want: "tampered with at #1"},
	}
	if runtime.GOOS != "windows" {
		tests = append(tests, struct {
			name   string
			prep   func(s *opsServer)
			vars   []string
			now    time.Duration
			item   string
			status string
			want   string
		}{name: "data directory open", prep: func(s *opsServer) { os.Chmod(s.dataDir, 0o755) }, item: "data directory", status: statusWarn, want: "chmod 0700"})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newOpsServer(t, nil)
			s.createAdmin("admin")
			if tt.prep != nil {
				tt.prep(s)
			}
			e, _, _ := s.env(tt.vars...)
			e.doctorDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return nil, errors.New("connection refused")
			}
			e.doctorNow = time.Now().Add(tt.now)
			code, status, out := doctorLines(t, e)
			if status[tt.item] != tt.status || !strings.Contains(out, tt.want) {
				t.Errorf("%s: %q, want %q mentioning %q\n%s", tt.item, status[tt.item], tt.status, tt.want, out)
			}
			if wantCode := map[bool]int{true: exitFailure, false: exitOK}[tt.status == statusFail]; code != wantCode {
				t.Errorf("exit %d, want %d\n%s", code, wantCode, out)
			}
		})
	}
}

// TestOpsCommandsNextToARunningServer: the commands that only read run next to a serving
// server without disturbing it.
func TestOpsCommandsNextToARunningServer(t *testing.T) {
	s := newOpsServer(t, nil)
	s.createAdmin("admin")
	e, _, _ := s.env()
	srv := startServe(t, e)
	for _, args := range [][]string{{"backup"}, {"audit", "verify"}, {"config", "check"}} {
		if code, stdout, stderr := s.run(args...); code != exitOK {
			t.Errorf("%v: %d %s %s", args, code, stdout, stderr)
		}
	}
	de, _, _ := s.env()
	if code, status, out := doctorLines(t, de); code != exitOK || status["database"] != statusOK {
		t.Errorf("doctor: %d\n%s", code, out)
	}
	resp, body := get(t, "http://"+srv.addr+"/readyz")
	srv.wait(t)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("readyz: %s", body)
	}
}
