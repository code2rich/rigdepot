// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
	"github.com/code2rich/rigdepot/test/internal/binary"
)

// server is a real rgd-server on a loopback port, with a fresh database.
type server struct {
	url string
	cmd *exec.Cmd
	log bytes.Buffer
}

const setupToken = "<placeholder: setup token>"

// startServer starts it with its data in dir; env adds to its environment.
func startServer(t *testing.T, dir string, env ...string) *server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	s := &server{url: "http://" + addr}
	s.cmd = exec.Command(binary.BuildServer(t), "serve")
	s.cmd.Env = append(os.Environ(),
		brand.EnvPrefix+"BASE_URL="+s.url,
		brand.EnvPrefix+"LISTEN="+addr,
		brand.EnvPrefix+"DATA_DIR="+filepath.Join(dir, "server"),
		brand.EnvPrefix+"SETUP_TOKEN="+setupToken,
	)
	s.cmd.Env = append(s.cmd.Env, env...)
	s.cmd.Stdout, s.cmd.Stderr = &s.log, &s.log
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if runtime.GOOS == "windows" {
			s.cmd.Process.Kill()
		} else {
			s.cmd.Process.Signal(os.Interrupt)
		}
		s.cmd.Wait()
		if t.Failed() {
			t.Logf("server log:\n%s", s.log.String())
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(s.url + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server did not get ready: %v\n%s", err, s.log.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// browser is a person's browser signed in to the server: a cookie jar.
type browser struct {
	t      *testing.T
	base   string
	client *http.Client
}

// firstAdmin creates the first administrator with the setup token and
// signs the browser in as them.
func (s *server) firstAdmin(t *testing.T) *browser {
	t.Helper()
	b := s.newBrowser(t)
	body := `{"username":"admin","display_name":"Administrator","password":"<placeholder: admin password>"}`
	if code, out := b.do(http.MethodPost, "/api/v1/setup/admin", body, map[string]string{"X-Setup-Token": setupToken}); code != http.StatusCreated {
		t.Fatalf("first administrator: %d %s", code, out)
	}
	return b
}

// newBrowser is a browser signed in nowhere yet. It does not follow redirects, so that a test
// sees where a sign-in sends it.
func (s *server) newBrowser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: s.url, client: &http.Client{Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) do(method, path, body string, header map[string]string) (int, []byte) {
	b.t.Helper()
	resp, out := b.send(method, path, body, header)
	return resp.StatusCode, out
}

// send makes one request and returns the answer with its body read.
func (b *browser) send(method, path, body string, header map[string]string) (*http.Response, []byte) {
	b.t.Helper()
	req, err := http.NewRequest(method, b.base+path, strings.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func (b *browser) devices() []map[string]any {
	b.t.Helper()
	code, out := b.do(http.MethodGet, "/api/v1/me/devices", "", nil)
	var list struct{ Items []map[string]any }
	if code != http.StatusOK || json.Unmarshal(out, &list) != nil {
		b.t.Fatalf("my devices: %d %s", code, out)
	}
	return list.Items
}

var userCode = regexp.MustCompile(`code: ([A-Z0-9]{4}-[A-Z0-9]{4})`)

// The device code sequence end to end (docs/architecture/06-server.md#rgd-login设备码流程):
// the real rgd logs in to the real rgd-server, the person approves in a
// signed-in browser, the computer shows up among the person's devices, and
// rgd logout signs it out there.
func TestLoginWithDeviceCode(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real programs, and waits for one poll interval")
	}
	root := filepath.Join(testenv.Current(t).Home, t.Name())
	srv := startServer(t, root)
	b := srv.firstAdmin(t)

	userHome := filepath.Join(root, "home")
	if err := os.MkdirAll(userHome, 0o700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "HOME="+userHome, "USERPROFILE="+userHome, brand.EnvPrefix+"LANG=en", "NO_COLOR=1")
	rgd := binary.Build(t)

	// rgd login prints the code and waits.
	login := exec.Command(rgd, "login", srv.url)
	login.Env, login.Dir = env, userHome
	login.Stdin = strings.NewReader("")
	var stdout, printed lockedBuffer
	login.Stdout, login.Stderr = &stdout, &printed
	if err := login.Start(); err != nil {
		t.Fatal(err)
	}
	code := ""
	for deadline := time.Now().Add(30 * time.Second); code == "" && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if m := userCode.FindStringSubmatch(printed.String()); m != nil {
			code = m[1]
		}
	}
	if code == "" {
		login.Process.Kill()
		login.Wait()
		t.Fatalf("no code printed:\n%s", printed.String())
	}
	if !strings.Contains(printed.String(), srv.url+"/device?code="+code) {
		t.Errorf("the confirmation page is not printed:\n%s", printed.String())
	}

	// The browser shows the request and approves it.
	status, out := b.do(http.MethodGet, "/api/v1/device/requests/"+code, "", nil)
	var req map[string]any
	json.Unmarshal(out, &req)
	if status != http.StatusOK || req["status"] != "pending" || req["os"] != runtime.GOOS {
		t.Fatalf("request: %d %s", status, out)
	}
	if status, out := b.do(http.MethodPost, "/api/v1/device/requests/"+code+"/approve", "", nil); status != http.StatusNoContent {
		t.Fatalf("approve: %d %s", status, out)
	}

	done := make(chan error, 1)
	go func() { done <- login.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("rgd login: %v\n%s%s", err, stdout.String(), printed.String())
		}
	case <-time.After(60 * time.Second):
		login.Process.Kill()
		t.Fatalf("rgd login did not finish:\n%s", printed.String())
	}
	if !strings.Contains(stdout.String(), "Signed in to "+srv.url+" as Administrator (admin)") {
		t.Errorf("rgd login said: %s", stdout.String())
	}

	// The computer is now among the person's devices.
	devs := b.devices()
	if len(devs) != 1 || devs[0]["os"] != runtime.GOOS || devs[0]["current"] != false {
		t.Fatalf("devices after login: %v", devs)
	}

	// rgd logout signs it out on the server.
	logout := exec.Command(rgd, "--json", "logout")
	logout.Env, logout.Dir = env, userHome
	out, err := logout.Output()
	if err != nil {
		t.Fatalf("rgd logout: %v %s", err, out)
	}
	var doc map[string]any
	if json.Unmarshal(out, &doc) != nil || doc["serverSignedOut"] != true {
		t.Errorf("rgd logout: %s", out)
	}
	if devs := b.devices(); len(devs) != 0 {
		t.Errorf("devices after logout: %v", devs)
	}
}

// lockedBuffer is a buffer a child process writes to while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
