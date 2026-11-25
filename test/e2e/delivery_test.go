// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package e2e

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/testenv"
)

// publishSkill uploads a skill as the browser's person and publishes it.
func (b *browser) publishSkill(code, body string) {
	b.t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range map[string]string{
		code + "/SKILL.md":  "---\nname: " + code + "\ndescription: " + body + "\n---\n\n" + body + "\n",
		code + "/meta.yaml": "name: " + code + "\nwhen: When it helps\n",
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(data))
	}
	zw.Close()
	req, _ := http.NewRequest(http.MethodPut, b.base+"/api/v1/resources/"+code+"/draft/bundle?kind=skill", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	resp.Body.Close()
	etag := resp.Header.Get("ETag")
	if resp.StatusCode != http.StatusCreated || etag == "" {
		b.t.Fatalf("upload %s: %d", code, resp.StatusCode)
	}
	if status, out := b.do(http.MethodPost, "/api/v1/resources/"+code+"/publish", `{}`, map[string]string{"If-Match": etag}); status != http.StatusCreated {
		b.t.Fatalf("publish %s: %d %s", code, status, out)
	}
}

// get reads a JSON answer of the API.
func (b *browser) get(path string, v any) {
	b.t.Helper()
	status, out := b.do(http.MethodGet, path, "", nil)
	if status != http.StatusOK || json.Unmarshal(out, v) != nil {
		b.t.Fatalf("GET %s: %d %s", path, status, out)
	}
}

// login signs the world's computer in to the server as the browser's person: rgd login, the
// browser approves the code it prints.
func (w *world) login(srv *server, b *browser) {
	w.t.Helper()
	cmd := exec.Command(w.rgd, "login", srv.url)
	cmd.Env, cmd.Dir, cmd.Stdin = w.env, w.home, strings.NewReader("")
	var stdout, printed lockedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &printed
	if err := cmd.Start(); err != nil {
		w.t.Fatal(err)
	}
	code := ""
	for deadline := time.Now().Add(30 * time.Second); code == "" && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if m := userCode.FindStringSubmatch(printed.String()); m != nil {
			code = m[1]
		}
	}
	if code == "" {
		cmd.Process.Kill()
		cmd.Wait()
		w.t.Fatalf("no code printed:\n%s", printed.String())
	}
	if status, out := b.do(http.MethodPost, "/api/v1/device/requests/"+code+"/approve", "", nil); status != http.StatusNoContent {
		w.t.Fatalf("approve: %d %s", status, out)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			w.t.Fatalf("rgd login: %v\n%s%s", err, stdout.String(), printed.String())
		}
	case <-time.After(60 * time.Second):
		cmd.Process.Kill()
		w.t.Fatalf("rgd login did not finish:\n%s", printed.String())
	}
}

// stop stops the server for good.
func (s *server) stop() {
	if runtime.GOOS == "windows" {
		s.cmd.Process.Kill()
	} else {
		s.cmd.Process.Signal(os.Interrupt)
	}
	s.cmd.Wait()
}

// TestSyncFromTheServer is M1-27 end to end: the real rgd signs in to the real rgd-server, syncs
// what the server publishes into a home with three AI tools, reports what it installed, and the
// server shows it. A choice made with rgd disable reaches the server; when the server cannot be
// reached, sync works from what the server said last and removes nothing, and uninstall still
// restores every byte.
func TestSyncFromTheServer(t *testing.T) {
	w := newWorld(t)
	before := snapshot(t, w.home)
	srv := startServer(t, filepath.Join(testenv.Current(t).Home, t.Name()))
	b := srv.firstAdmin(t)
	b.publishSkill("code-review", "Review a change before it is merged.")
	b.publishSkill("unit-testing", "Write unit tests for a change.")

	// Not signed in and no repository: the program says to sign in.
	r := w.must(2, "sync")
	if !strings.Contains(r.stderr, "login") {
		t.Errorf("without a sign-in or a repository:\n%s", r.stderr)
	}
	w.login(srv, b)

	// The plan and the sync come from the server.
	r = w.must(exitOK, "sync", "--plan")
	if !strings.Contains(r.stdout, "code-review") || !strings.Contains(r.stdout, "unit-testing") {
		t.Errorf("sync --plan:\n%s", r.stdout)
	}
	w.must(exitOK, "sync")
	for _, tool := range []string{"claude", "codex"} {
		for _, code := range []string{"code-review", "unit-testing"} {
			if !strings.Contains(string(w.read(skillDir(tool, code)+"/SKILL.md")), "name: "+code) {
				t.Errorf("%s: not the server's skill", skillDir(tool, code))
			}
		}
	}
	w.must(exitOK, "status")

	// The server knows what is installed where, without any path.
	var resource struct {
		InstalledPeople int `json:"installed_people"`
		MyInstalls      []struct {
			Tool, Status string
			Version      int
		} `json:"my_installs"`
	}
	b.get("/api/v1/catalog/unit-testing", &resource)
	tools := []string{}
	for _, in := range resource.MyInstalls {
		if in.Status == "installed" && in.Version == 1 {
			tools = append(tools, in.Tool)
		}
	}
	if resource.InstalledPeople != 1 || !strings.Contains(strings.Join(tools, ","), "claude-code") || !strings.Contains(strings.Join(tools, ","), "codex") {
		t.Errorf("unit-testing on the server: %+v", resource)
	}
	var devices struct {
		Items []struct {
			LastSync *struct{ Result string } `json:"last_sync"`
			Tools    []struct{ Tool string }  `json:"tools"`
		}
	}
	b.get("/api/v1/me/devices", &devices)
	if len(devices.Items) != 1 || devices.Items[0].LastSync == nil || devices.Items[0].LastSync.Result != "ok" || len(devices.Items[0].Tools) != 3 {
		t.Errorf("my devices: %+v", devices)
	}
	var summary struct {
		People  int `json:"people_connected"`
		Devices int `json:"devices_active"`
	}
	b.get("/api/v1/installs/summary", &summary)
	if summary.People != 1 || summary.Devices != 1 {
		t.Errorf("summary: %+v", summary)
	}
	status, out := b.do(http.MethodGet, "/api/v1/me/devices", "", nil)
	if status != http.StatusOK || strings.Contains(string(out), w.home) || strings.Contains(string(out), ".claude") {
		t.Errorf("a path reached the server: %s", out)
	}

	// rgd disable turns code-review off on the server, for every computer of the person.
	r = w.must(exitOK, "--json", "disable", "code-review")
	var sel map[string]any
	if json.Unmarshal([]byte(r.stdout), &sel) != nil || sel["changed"] != true || sel["server"] != srv.url {
		t.Errorf("rgd disable: %s", r.stdout)
	}
	var catalog struct{ Enabled bool }
	b.get("/api/v1/catalog/code-review", &catalog)
	if catalog.Enabled {
		t.Error("code-review is still on for the person on the server")
	}
	// A preview takes the server's new answer in, without acting on it.
	r = w.must(exitOK, "sync", "--plan")
	if !strings.Contains(r.stdout, "code-review") {
		t.Errorf("the plan does not remove code-review:\n%s", r.stdout)
	}

	// The server goes away. A sync uses what the server said last, which no longer has
	// code-review, and removes nothing.
	srv.stop()
	r = w.must(exitOK, "sync")
	if !strings.Contains(r.stderr, "cannot be reached") {
		t.Errorf("no word that the server cannot be reached:\n%s", r.stderr)
	}
	for _, tool := range []string{"claude", "codex"} {
		if !w.exists(skillDir(tool, "code-review") + "/SKILL.md") {
			t.Errorf("%s was removed while the server could not be reached", skillDir(tool, "code-review"))
		}
	}
	w.must(exitOK, "status")

	// Uninstall needs no server.
	w.must(exitOK, "uninstall")
	after := snapshot(t, w.home)
	if d := diffTrees(before, after.without(dataDir)); d != "" {
		t.Fatalf("after uninstall the home directory is not what it was:\n%s", d)
	}
}
