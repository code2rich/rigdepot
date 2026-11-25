// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/catalog/catalogtest"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// localRepo makes a git repository with one skill and returns its file:// address.
func localRepo(t *testing.T) string {
	t.Helper()
	git, dir := testenv.NeedGit(t), t.TempDir()
	for name, content := range map[string]string{
		"rigdepot.yaml":                   "schema: 1\n",
		"skills/unit-testing/SKILL.md":    "---\nname: unit-testing\ndescription: Writes unit tests.\n---\nSteps.\n",
		"skills/unit-testing/meta.yaml":   "name: Unit testing\nwhen: When a change needs tests\n",
		"skills/broken/SKILL.md":          "---\nname: broken\n---\n",
		"skills/code-review/SKILL.md":     "---\nname: code-review\ndescription: Reviews.\n---\nLook.\n",
		"skills/code-review/meta.yaml":    "name: Code review\nwhen: Before merging\n",
		"skills/code-review/extra/x.md":   "x\n",
		"skills/unit-testing/extra/y.txt": "y\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "--quiet", "--initial-branch=main"}, {"add", "--all"}, {"commit", "--quiet", "--message", "first"}} {
		cmd := exec.Command(git, append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=dev@example.com",
			"GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=dev@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	p := filepath.ToSlash(dir)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file://" + p
}

func TestSourcesAPI(t *testing.T) {
	const token = "<placeholder-read-only-token>"
	af := newAPIWith(t, fixtureOptions{background: true, setup: func(app *App) {
		app.catalogOptions.Kinds, app.catalogOptions.Rules = catalogtest.Kinds(), catalogtest.Rules()
		app.catalogOptions.AllowLocalSources = true
		app.cfg.Git = testenv.NeedGit(t)
	}})
	f := &catalogFixture{apiFixture: af, t: t}
	url := localRepo(t)

	f.call("GET", "/api/v1/sources", "", nil, nil, http.StatusUnauthorized)
	f.call("GET", "/api/v1/sources", testMember, nil, nil, http.StatusForbidden)
	f.call("POST", "/api/v1/sources", testMember, jsonBody(map[string]any{"name": "r", "url": url, "branch": "main"}), nil, http.StatusForbidden)
	f.call("POST", "/api/v1/sources", testAdmin, jsonBody(map[string]any{"name": "r", "url": "http://git.example.com/r.git", "branch": "main"}), nil, http.StatusBadRequest)

	// An https repository with a token: has_credential, never the token itself.
	rec := f.call("POST", "/api/v1/sources", testAdmin, jsonBody(map[string]any{"name": "remote", "url": "https://127.0.0.1:1/r.git",
		"branch": "main", "credential": token}), nil, http.StatusCreated)
	remote := decode[httpapi.Source](t, rec)
	if !remote.HasCredential || strings.Contains(rec.Body.String(), token) || rec.Header().Get("ETag") != remote.Etag {
		t.Fatalf("create with a credential: %s", rec.Body)
	}
	// Its pull fails at once: nothing listens on port 1 of this computer.
	f.call("DELETE", "/api/v1/sources/"+remote.Id, testAdmin, nil, nil, http.StatusNoContent)

	rec = f.call("POST", "/api/v1/sources", testAdmin, jsonBody(map[string]any{"name": "harness-resources", "url": url, "branch": "main"}), nil, http.StatusCreated)
	src := decode[httpapi.Source](t, rec)
	if src.LastPull == nil || src.LastPull.Result != httpapi.SourcePullResultRunning {
		t.Fatalf("a new source starts pulling: %s", rec.Body)
	}
	src = f.waitForPull(src.Id)
	if src.LastPull.Result != httpapi.SourcePullResultOk || *src.LastPull.Imported != 2 || *src.LastPull.Failed != 1 || src.ResourceCount != 2 {
		t.Fatalf("after the first pull: %+v", *src.LastPull)
	}
	rec = f.call("GET", "/api/v1/resources/unit-testing", testAdmin, nil, nil, http.StatusOK)
	r := decode[httpapi.ManagedResource](t, rec)
	if r.Source == nil || r.Source.Id != src.Id || r.Source.Path != "skills/unit-testing" || r.Origin != httpapi.OriginRepo {
		t.Errorf("imported resource: %s", rec.Body)
	}

	// Pull now: queued, then done again with nothing new.
	rec = f.call("POST", "/api/v1/sources/"+src.Id+"/pull", testAdmin, nil, nil, http.StatusAccepted)
	if decode[httpapi.Source](t, rec).LastPull.Result != httpapi.SourcePullResultRunning {
		t.Errorf("pull now: %s", rec.Body)
	}
	src = f.waitForPull(src.Id)
	if src.LastPull.Result != httpapi.SourcePullResultOk || *src.LastPull.Imported != 0 {
		t.Errorf("second pull: %+v", *src.LastPull)
	}

	patch := "/api/v1/sources/" + src.Id
	f.call("PATCH", patch, testAdmin, jsonBody(map[string]any{"name": "renamed"}), nil, http.StatusPreconditionRequired)
	f.call("PATCH", patch, testAdmin, jsonBody(map[string]any{"name": "renamed"}), map[string]string{"If-Match": `"s-x-0"`}, http.StatusPreconditionFailed)
	rec = f.call("PATCH", patch, testAdmin, jsonBody(map[string]any{"name": "renamed"}), map[string]string{"If-Match": src.Etag}, http.StatusOK)
	if decode[httpapi.Source](t, rec).Name != "renamed" {
		t.Errorf("rename: %s", rec.Body)
	}
	rec = f.call("GET", "/api/v1/sources", testAdmin, nil, nil, http.StatusOK)
	list := decode[httpapi.SourceList](t, rec)
	if len(list.Items) != 1 {
		t.Fatalf("list: %s", rec.Body)
	}
	f.call("PUT", "/api/v1/sources/order", testAdmin, jsonBody(map[string]any{"ids": []string{}}), nil, http.StatusBadRequest)
	f.call("PUT", "/api/v1/sources/order", testAdmin, jsonBody(map[string]any{"ids": []string{src.Id}}), nil, http.StatusOK)
	f.call("GET", "/api/v1/sources/"+testMember, testAdmin, nil, nil, http.StatusNotFound)

	// Deleting the source keeps what it imported.
	f.call("DELETE", patch, testAdmin, nil, nil, http.StatusNoContent)
	f.call("GET", "/api/v1/resources/unit-testing", testAdmin, nil, nil, http.StatusOK)
}

// waitForPull polls a source until its pull is no longer running.
func (f *catalogFixture) waitForPull(id string) httpapi.Source {
	f.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		rec := f.call("GET", "/api/v1/sources/"+id, testAdmin, nil, nil, http.StatusOK)
		src := decode[httpapi.Source](f.t, rec)
		if src.LastPull != nil && src.LastPull.Result != httpapi.SourcePullResultRunning {
			return src
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("the pull did not finish: %s", rec.Body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
