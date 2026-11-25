// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

// The delivery operations over the real routes, each answer checked against the contract: a
// person turning a resource off and failing to turn off a required one, the desired state and
// its ETag, the bundle, the sync report, and where the report shows up (my devices, my
// installs, the summary, the command-line versions).
func TestDeliveryAPI(t *testing.T) {
	f := newCatalogAPI(t)
	ctx := context.Background()
	now := dbx.Millis(time.Now())
	// The administrator's computer, signed in (the stand-in verifies its token).
	if _, err := f.app.db.ExecContext(ctx, `INSERT INTO devices (id, user_id, name, os, arch, cli_version, key_storage, public_key, token_digest, created_at, last_seen_at, issued_at)
		VALUES (?, ?, 'dev-laptop', 'darwin', 'arm64', '0.5.0', 'keyring', 'key', 'digest', ?, ?, ?)`, testDevice, testAdmin, now, now, now); err != nil {
		t.Fatal(err)
	}
	device := map[string]string{"Authorization": "Bearer " + f.deviceToken}
	withDevice := func(extra map[string]string) map[string]string {
		out := map[string]string{"Authorization": "Bearer " + f.deviceToken}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	// A published skill, recommended by default.
	rec := f.call("PUT", "/api/v1/resources/unit-testing/draft/bundle?kind=skill", testAdmin, skillZip(t, "Steps.\n"),
		map[string]string{"Content-Type": "application/octet-stream"}, http.StatusCreated)
	draft := decode[httpapi.Draft](t, rec)
	f.call("POST", "/api/v1/resources/unit-testing/publish", testAdmin, []byte(`{}`), map[string]string{"If-Match": draft.Etag}, http.StatusCreated)

	// Turning it off and on again, for oneself.
	pref := "/api/v1/catalog/unit-testing/preference"
	f.call("PUT", pref, "", jsonBody(map[string]bool{"enabled": false}), nil, http.StatusUnauthorized)
	rec = f.call("PUT", pref, testMember, jsonBody(map[string]bool{"enabled": false}), nil, http.StatusOK)
	if p := decode[httpapi.Preference](t, rec); p.Enabled || p.Policy != "recommended" {
		t.Errorf("turned off: %s", rec.Body)
	}
	if l := decode[httpapi.CatalogList](t, f.call("GET", "/api/v1/catalog", testMember, nil, nil, http.StatusOK)); len(l.Items) != 1 || l.Items[0].Enabled {
		t.Errorf("the catalog after turning it off: %+v", l)
	}
	f.call("PUT", pref, testMember, jsonBody(map[string]bool{"enabled": true}), nil, http.StatusOK)
	f.call("PUT", "/api/v1/catalog/no-such/preference", testMember, jsonBody(map[string]bool{"enabled": true}), nil, http.StatusNotFound)

	// The desired state: only with a device token (or an access token), with an ETag.
	f.call("GET", "/api/v1/sync/desired", testAdmin, nil, nil, http.StatusForbidden) // a browser session
	rec = f.call("GET", "/api/v1/sync/desired", "", nil, device, http.StatusOK)
	st := decode[httpapi.DesiredState](t, rec)
	etag := rec.Header().Get("ETag")
	if etag == "" || st.User.Username != "admin" || len(st.Resources) != 1 || st.Cli.MinVersion == "" || st.Defaults.McpMode != "single-entry" {
		t.Fatalf("desired state: %s %s", etag, rec.Body)
	}
	r := st.Resources[0]
	if r.Code != "unit-testing" || r.Version != 1 || r.Reason != "policy" || r.Size == 0 || r.Meta.Name != "Unit testing" {
		t.Errorf("desired resource: %+v", r)
	}
	f.call("GET", "/api/v1/sync/desired", "", nil, withDevice(map[string]string{"If-None-Match": etag}), http.StatusNotModified)

	// Settings that the command line obeys change the state, and the ETag with it.
	if _, err := f.app.db.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES ('adapters.disabled', '["opencode"]', 1)`); err != nil {
		t.Fatal(err)
	}
	rec = f.call("GET", "/api/v1/sync/desired", "", nil, withDevice(map[string]string{"If-None-Match": etag}), http.StatusOK)
	if st := decode[httpapi.DesiredState](t, rec); len(st.Defaults.ToolsDisabled) != 1 || rec.Header().Get("ETag") == etag {
		t.Errorf("after turning a tool off: %s %s", rec.Header().Get("ETag"), rec.Body)
	}

	// The bundle is the tar the digest names.
	rec = f.call("GET", "/api/v1/sync/bundles/"+r.Bundle, "", nil, device, http.StatusOK)
	if rec.Header().Get("Content-Type") != "application/x-tar" || rec.Body.Len() != r.Size {
		t.Errorf("bundle: %s, %d bytes", rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	names := []string{}
	tr := tar.NewReader(bytes.NewReader(rec.Body.Bytes()))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	if strings.Join(names, ",") != "SKILL.md,meta.yaml" {
		t.Errorf("bundle files: %v", names)
	}
	f.call("GET", "/api/v1/sync/bundles/sha256:"+strings.Repeat("0", 64), "", nil, device, http.StatusNotFound)

	// The report: device tokens only, no paths.
	report := map[string]any{
		"cli_version": "0.5.0", "os": "darwin", "arch": "arm64", "finished_at": "2026-11-25T10:31:00Z", "result": "ok",
		"tools":    []map[string]any{{"tool": "claude-code", "version": "2.1.330"}, {"tool": "codex"}},
		"installs": []map[string]any{{"code": "unit-testing", "version": 1, "tool": "claude-code", "status": "installed"}},
	}
	f.call("POST", "/api/v1/sync/report", testAdmin, jsonBody(report), nil, http.StatusForbidden)
	f.call("POST", "/api/v1/sync/report", "", jsonBody(map[string]any{
		"cli_version": "0.5.0", "os": "darwin", "arch": "arm64", "finished_at": "2026-11-25T10:31:00Z", "result": "failed", "tools": []any{},
		"installs": []map[string]any{{"code": "unit-testing", "version": 1, "tool": "codex", "status": "failed", "reason": "/Users/jdoe/.codex"}},
	}), device, http.StatusBadRequest)
	f.call("POST", "/api/v1/sync/report", "", jsonBody(report), device, http.StatusNoContent)

	// Where the report shows up.
	rec = f.call("GET", "/api/v1/me/devices", testAdmin, nil, nil, http.StatusOK)
	devs := decode[httpapi.DeviceList](t, rec)
	if len(devs.Items) != 1 || devs.Items[0].LastSync == nil || devs.Items[0].LastSync.Result != "ok" || len(devs.Items[0].Tools) != 2 {
		t.Errorf("my devices: %s", rec.Body)
	}
	rec = f.call("GET", "/api/v1/catalog/unit-testing", testAdmin, nil, nil, http.StatusOK)
	if c := decode[httpapi.CatalogResource](t, rec); c.InstalledPeople != 1 || len(c.MyInstalls) != 1 || c.MyInstalls[0].DeviceName != "dev-laptop" {
		t.Errorf("my installs: %s", rec.Body)
	}
	rec = f.call("GET", "/api/v1/installs/summary", testAdmin, nil, nil, http.StatusOK)
	sum := decode[httpapi.InstallsSummary](t, rec)
	if sum.PeopleConnected != 1 || sum.DevicesActive != 1 || len(sum.ByTool) != 2 || len(sum.CliVersions) != 1 || sum.CliVersions[0].Version != "0.5.0" {
		t.Errorf("summary: %s", rec.Body)
	}
	f.call("GET", "/api/v1/installs/summary", testMember, nil, nil, http.StatusForbidden)
	rec = f.call("GET", "/api/v1/cli", testAdmin, nil, nil, http.StatusOK)
	if p := decode[httpapi.CLIPolicy](t, rec); len(p.Distribution) != 1 || p.Distribution[0].People != 1 {
		t.Errorf("command-line versions: %s", rec.Body)
	}
	rec = f.call("GET", "/api/v1/resources/unit-testing/impact", testAdmin, nil, nil, http.StatusOK)
	if im := decode[httpapi.Impact](t, rec); im.People != 1 || im.Devices != 1 {
		t.Errorf("impact: %s", rec.Body)
	}

	// A required resource cannot be turned off.
	res := decode[httpapi.ManagedResource](t, f.call("GET", "/api/v1/resources/unit-testing", testAdmin, nil, nil, http.StatusOK))
	f.call("PATCH", "/api/v1/resources/unit-testing", testAdmin, jsonBody(map[string]string{"policy": "required"}), map[string]string{"If-Match": res.Etag}, http.StatusOK)
	rec = f.call("PUT", pref, testMember, jsonBody(map[string]bool{"enabled": false}), nil, http.StatusConflict)
	if p := decode[httpapi.Problem](t, rec); p.Code != "resource.required" {
		t.Errorf("required: %s", rec.Body)
	}
}
