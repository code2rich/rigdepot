// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// doctorWorld is a computer with a working git and nothing else.
func doctorWorld(t *testing.T) *world {
	w := newWorld(t)
	w.env[brand.EnvPrefix+"GIT"] = testenv.NeedGit(t)
	return w
}

func doctorResults(t *testing.T, doc map[string]any) map[string]map[string]any {
	t.Helper()
	if doc["schema"] != cliout.Schema("doctor", 1) {
		t.Errorf("schema = %v", doc["schema"])
	}
	list, _ := doc["results"].([]any)
	byCheck := map[string]map[string]any{}
	for _, item := range list {
		r, _ := item.(map[string]any)
		for _, field := range []string{"check", "status", "code", "title", "message"} {
			if s, _ := r[field].(string); s == "" {
				t.Errorf("result without %s: %v", field, r)
			}
		}
		if _, seen := byCheck[r["check"].(string)]; !seen {
			byCheck[r["check"].(string)] = r
		}
	}
	if _, ok := doc["summary"].(map[string]any); !ok {
		t.Errorf("no summary: %v", doc)
	}
	return byCheck
}

func TestDoctorOnAFreshComputer(t *testing.T) {
	w := doctorWorld(t)
	args := []string{"doctor", "--json"}
	r := w.run(args...)
	if r.code != int(cliout.ExitOK) {
		t.Errorf("exit code %d; stderr %s", r.code, r.stderr)
	}
	results := doctorResults(t, oneDocument(t, args, r.stdout))
	for check, code := range map[string]string{"home": "missing", "config": "missing", "manifest": "missing", "git": "ok"} {
		if results[check]["code"] != code {
			t.Errorf("%s: %v, want code %s", check, results[check], code)
		}
	}
	// Looking created nothing.
	if _, err := os.Lstat(w.dataDir()); !os.IsNotExist(err) {
		t.Errorf("doctor created the local data directory: %v", err)
	}
}

// What stops every other command is what doctor is for: it runs, reports, and exits with 1.
func TestDoctorReportsWhatIsBroken(t *testing.T) {
	w := doctorWorld(t)
	w.writeConfig(t, "lang = \"en\"\n[repo\n")
	if err := os.WriteFile(filepath.Join(w.dataDir(), home.ManifestFile), []byte(`{"schema":1,"entries":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--json", "doctor"}
	r := w.run(args...)
	if r.code != int(cliout.ExitFailure) {
		t.Errorf("exit code %d, want %d", r.code, cliout.ExitFailure)
	}
	results := doctorResults(t, oneDocument(t, args, r.stdout))
	if results["config"]["code"] != "unreadable" || results["config"]["status"] != "fail" {
		t.Errorf("config: %v", results["config"])
	}
	if results["manifest"]["code"] != "manifest_malformed" || results["manifest"]["status"] != "fail" {
		t.Errorf("manifest: %v", results["manifest"])
	}

	r = w.run("--lang", "zh-CN", "doctor")
	if r.code != int(cliout.ExitFailure) {
		t.Errorf("exit code %d", r.code)
	}
	for _, want := range []string{"清单", "本机配置", " x ", "项未通过"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("human output lacks %q:\n%s", want, r.stdout)
		}
	}
	if r.stderr != "" {
		t.Errorf("stderr: %s", r.stderr)
	}
}
