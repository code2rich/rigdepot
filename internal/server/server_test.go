// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/config"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// syncBuffer is a bytes.Buffer that the server goroutine and the test can
// share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// testEnv returns an env whose variables are vars on top of a minimal local
// configuration in a temporary data directory.
func testEnv(t *testing.T, vars map[string]string) (*env, *syncBuffer, *syncBuffer) {
	t.Helper()
	all := map[string]string{
		config.EnvName("BASE_URL"): "http://127.0.0.1:8080",
		config.EnvName("LISTEN"):   "127.0.0.1:0",
		config.EnvName("DATA_DIR"): filepath.Join(t.TempDir(), "data"),
	}
	for k, v := range vars {
		all[k] = v
	}
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	return &env{
		stdout: stdout, stderr: stderr,
		lookupEnv: func(k string) (string, bool) { v, ok := all[k]; return v, ok },
	}, stdout, stderr
}

// running is a server started by startServe.
type running struct {
	addr   string
	stop   context.CancelFunc
	done   chan struct{} // closed when run returned
	code   int
	stdout *syncBuffer
	stderr *syncBuffer
}

// wait stops the server and returns its exit code.
func (r *running) wait(t *testing.T) int {
	t.Helper()
	r.stop()
	select {
	case <-r.done:
		return r.code
	case <-time.After(30 * time.Second):
		t.Fatal("the server did not stop")
		return -1
	}
}

func startServe(t *testing.T, e *env, args ...string) *running {
	t.Helper()
	listening := make(chan string, 1)
	e.listening = listening
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{stop: cancel, done: make(chan struct{}), stdout: e.stdout.(*syncBuffer), stderr: e.stderr.(*syncBuffer)}
	go func() {
		r.code = run(ctx, append([]string{"serve"}, args...), e)
		close(r.done)
	}()
	select {
	case r.addr = <-listening:
	case <-r.done:
		cancel()
		t.Fatalf("serve exited with %d before listening:\n%s%s", r.code, r.stdout, r.stderr)
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("serve did not start listening")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(30 * time.Second):
		}
	})
	return r
}

// runBounded runs a command that is expected to end by itself. If it does
// not (a server that should have refused to start), it is stopped after a
// while so that the test fails instead of hanging.
func runBounded(e *env, args ...string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return run(ctx, args, e)
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestVersion(t *testing.T) {
	for _, arg := range []string{"--version", "version"} {
		e, stdout, stderr := testEnv(t, nil)
		if code := run(context.Background(), []string{arg}, e); code != exitOK {
			t.Errorf("%s: exit code %d, want 0", arg, code)
		}
		if !strings.HasPrefix(stdout.String(), brand.Server+" ") || stderr.String() != "" {
			t.Errorf("%s: stdout = %q, stderr = %q; want the version on stdout only", arg, stdout, stderr)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"frobnicate"}, {"version", "extra"}, {"serve", "extra"}, {"serve", "--no-such-flag"}, {"keys"}} {
		e, stdout, stderr := testEnv(t, nil)
		if code := run(context.Background(), args, e); code != exitUsage {
			t.Errorf("%v: exit code %d, want %d", args, code, exitUsage)
		}
		if stdout.String() != "" || stderr.String() == "" {
			t.Errorf("%v: stdout = %q, stderr = %q; want an explanation on stderr only", args, stdout, stderr)
		}
	}
}

func TestHelpListsEveryCommand(t *testing.T) {
	e, stdout, _ := testEnv(t, nil)
	if code := run(context.Background(), []string{"help"}, e); code != exitOK {
		t.Fatalf("exit code %d", code)
	}
	for _, c := range commands() {
		if !strings.Contains(stdout.String(), c.name) {
			t.Errorf("help does not mention %q", c.name)
		}
	}
}

// TestOperationsCommandsSayTheyAreNotThereYet: the commands of task M1-42
// exist so that they fail clearly instead of as unknown commands.
func TestOperationsCommandsSayTheyAreNotThereYet(t *testing.T) {
	for _, args := range [][]string{{"backup"}, {"restore", "--check", "x"}, {"reset-admin"}, {"keys", "rotate"},
		{"audit", "verify"}, {"setup", "--from", "x"}, {"config", "check"}, {"doctor"}} {
		e, _, stderr := testEnv(t, nil)
		if code := run(context.Background(), args, e); code != exitFailure || !strings.Contains(stderr.String(), "not implemented") {
			t.Errorf("%v: exit code %d, stderr %q", args, code, stderr)
		}
	}
}

// TestServe starts the real server on a random loopback port and checks the
// probes, the empty API mount, request IDs and the request log.
func TestServe(t *testing.T) {
	e, _, _ := testEnv(t, nil)
	srv := startServe(t, e)

	resp, body := get(t, "http://"+srv.addr+"/healthz")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"ok"`) {
		t.Errorf("healthz: %s %s", resp.Status, body)
	}
	if resp.Header.Get(RequestIDHeader) == "" {
		t.Error("no request ID header")
	}

	resp, body = get(t, "http://"+srv.addr+"/readyz")
	var ready readyStatus
	if err := json.Unmarshal([]byte(body), &ready); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || ready.Status != "ok" || ready.Checks["database"] != "ok" || ready.Checks["migrations"] != "ok" {
		t.Errorf("readyz: %s %s", resp.Status, body)
	}

	resp, body = get(t, "http://"+srv.addr+"/api/v1/meta?token=abc")
	var p problem
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Content-Type") != "application/problem+json" ||
		p.Code != "not_found" || p.RequestID != resp.Header.Get(RequestIDHeader) {
		t.Errorf("api: %s %v %s", resp.Status, resp.Header, body)
	}

	if code := srv.wait(t); code != exitOK {
		t.Errorf("exit code %d after a graceful shutdown\n%s", code, srv.stdout)
	}
	logs := srv.stdout.String()
	for _, want := range []string{`"msg":"request"`, `"route":"GET /healthz"`, `"status":404`, `"request_id":"` + p.RequestID + `"`, `"msg":"stopped"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("log is missing %s:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "token=abc") || strings.Contains(logs, "/api/v1/meta") {
		t.Errorf("the log has the real path or query:\n%s", logs)
	}
	if _, err := os.Stat(filepath.Join(e.lookupEnvValue(config.EnvName("DATA_DIR")), brand.CLILong+".db")); err != nil {
		t.Errorf("no database in the data directory: %v", err)
	}
}

func (e *env) lookupEnvValue(k string) string { v, _ := e.lookupEnv(k); return v }

// TestPlainHTTPOnTheNetworkIsRefused is the acceptance check at the level
// of the program: the server does not start.
func TestPlainHTTPOnTheNetworkIsRefused(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"all interfaces":  {config.EnvName("LISTEN"): "0.0.0.0:0"},
		"http public url": {config.EnvName("BASE_URL"): "http://hub.example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			e, stdout, stderr := testEnv(t, vars)
			// Bounded: if the refusal is broken, the server starts, and the
			// test must fail rather than hang.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if code := run(ctx, []string{"serve"}, e); code != exitUsage {
				t.Errorf("exit code %d, want %d", code, exitUsage)
			}
			if !strings.Contains(stderr.String(), "plain HTTP") || stdout.String() != "" {
				t.Errorf("stderr %q stdout %q", stderr, stdout)
			}
		})
	}
	// Declared behind a reverse proxy: starts.
	e, _, _ := testEnv(t, map[string]string{config.EnvName("LISTEN"): "0.0.0.0:0", config.EnvName("BASE_URL"): "https://hub.example.com", config.EnvName("TRUSTED_PROXIES"): "127.0.0.1"})
	srv := startServe(t, e)
	_, port, _ := net.SplitHostPort(srv.addr)
	if resp, _ := get(t, "http://127.0.0.1:"+port+"/healthz"); resp.StatusCode != http.StatusOK {
		t.Errorf("healthz %s", resp.Status)
	}
}

func TestFlagsOverrideTheEnvironment(t *testing.T) {
	e, _, _ := testEnv(t, map[string]string{config.EnvName("LOG_FORMAT"): "json"})
	srv := startServe(t, e, "--log-format", "text")
	srv.wait(t)
	if !strings.Contains(srv.stdout.String(), "msg=starting") {
		t.Errorf("the --log-format flag did not win:\n%s", srv.stdout)
	}
}

func TestMountedAPIAndRouteInTheLog(t *testing.T) {
	e, _, _ := testEnv(t, map[string]string{config.EnvName("BASE_URL"): "http://127.0.0.1:8080/hub/"})
	var seen string
	e.mounts = func(*App) Mounts {
		return Mounts{API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.URL.Path
			logging.SetRoute(r.Context(), "/api/v1/resources/{code}")
			logging.SetUser(r.Context(), "u-1")
			w.WriteHeader(http.StatusTeapot)
		})}
	}
	srv := startServe(t, e)
	if resp, _ := get(t, "http://"+srv.addr+"/hub/api/v1/resources/secret-name"); resp.StatusCode != http.StatusTeapot {
		t.Errorf("mounted API: %s", resp.Status)
	}
	if seen != "/api/v1/resources/secret-name" {
		t.Errorf("the API saw path %q, want it without the base path", seen)
	}
	for _, path := range []string{"/hub/healthz", "/healthz", "/hub/readyz", "/readyz"} {
		if resp, _ := get(t, "http://"+srv.addr+path); resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %s", path, resp.Status)
		}
	}
	if resp, _ := get(t, "http://"+srv.addr+"/api/v1/x"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("outside the base path: %s", resp.Status)
	}
	srv.wait(t)
	logs := srv.stdout.String()
	if !strings.Contains(logs, `"route":"/api/v1/resources/{code}"`) || !strings.Contains(logs, `"user_id":"u-1"`) || strings.Contains(logs, "secret-name") {
		t.Errorf("request log:\n%s", logs)
	}
}

func TestGracefulShutdownLetsRequestsFinish(t *testing.T) {
	e, _, _ := testEnv(t, nil)
	started := make(chan struct{})
	e.mounts = func(*App) Mounts {
		return Mounts{API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			time.Sleep(300 * time.Millisecond)
			io.WriteString(w, "finished")
		})}
	}
	srv := startServe(t, e)
	type result struct {
		body string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + srv.addr + "/api/slow")
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		got <- result{string(b), err}
	}()
	<-started
	if code := srv.wait(t); code != exitOK {
		t.Errorf("exit code %d", code)
	}
	if r := <-got; r.err != nil || r.body != "finished" {
		t.Errorf("the request in flight got %q, %v", r.body, r.err)
	}
	if _, err := net.DialTimeout("tcp", srv.addr, time.Second); err == nil {
		t.Error("still listening after shutdown")
	}
}

func TestReadiness(t *testing.T) {
	cfg, err := config.Load(config.Options{LookupEnv: func(k string) (string, bool) {
		if k == config.EnvName("BASE_URL") {
			return "http://127.0.0.1", true
		}
		return "", false
	}})
	if err != nil {
		t.Fatal(err)
	}
	ready := func(app *App) (int, readyStatus) {
		rec := &recorder{header: http.Header{}}
		app.Handler().ServeHTTP(rec, mustRequest(t, "/readyz"))
		var s readyStatus
		if err := json.Unmarshal(rec.body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		return rec.code, s
	}

	db := dbxtest.Open(t, dbxtest.SQLiteURL(t))
	app := NewApp(cfg, logging.Discard(), db, Mounts{})
	if code, s := ready(app); code != http.StatusServiceUnavailable || s.Checks["migrations"] != "failed" {
		t.Errorf("before migrating: %d %+v", code, s)
	}
	if _, err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	app.AddReadyCheck(ReadyCheck{Name: "mail", Check: func(context.Context) error { return errors.New("not configured") }})
	if code, s := ready(app); code != http.StatusOK || s.Status != "degraded" || s.Checks["mail"] != "degraded" {
		t.Errorf("degraded: %d %+v", code, s)
	}
	app.draining.Store(true)
	if code, s := ready(app); code != http.StatusServiceUnavailable || s.Status != "unavailable" {
		t.Errorf("draining: %d %+v", code, s)
	}
	app.draining.Store(false)
	db.Close()
	if code, s := ready(app); code != http.StatusServiceUnavailable || s.Checks["database"] != "failed" {
		t.Errorf("database closed: %d %+v", code, s)
	}
}

func TestPanicIsA500(t *testing.T) {
	cfg := &config.Config{}
	cfg.BaseURL, _ = cfg.BaseURL.Parse("http://127.0.0.1")
	var logs syncBuffer
	app := &App{cfg: cfg, log: logging.New(&logs, nil, logging.JSON), mounts: Mounts{API: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})}}
	rec := &recorder{header: http.Header{}}
	app.Handler().ServeHTTP(rec, mustRequest(t, "/api/x"))
	if rec.code != http.StatusInternalServerError || !strings.Contains(logs.String(), "handler panicked") {
		t.Errorf("code %d, logs %s", rec.code, logs.String())
	}
}

func TestAutoMigrateOff(t *testing.T) {
	e, _, _ := testEnv(t, map[string]string{config.EnvName("AUTO_MIGRATE"): "false"})
	if code := runBounded(e, "serve"); code != exitFailure {
		t.Fatalf("serve on a database with pending migrations: exit code %d, want %d", code, exitFailure)
	}
	if !strings.Contains(e.stdout.(*syncBuffer).String(), "migrate") {
		t.Errorf("the log does not say to run migrate:\n%s", e.stdout)
	}
	if code := run(context.Background(), []string{"migrate"}, e); code != exitOK {
		t.Fatalf("migrate: exit code %d\n%s", code, e.stdout)
	}
	srv := startServe(t, e)
	if resp, _ := get(t, "http://"+srv.addr+"/readyz"); resp.StatusCode != http.StatusOK {
		t.Errorf("readyz after migrate: %s", resp.Status)
	}
}

func TestRefusesADatabaseFromANewerVersion(t *testing.T) {
	e, _, _ := testEnv(t, nil)
	if code := run(context.Background(), []string{"migrate"}, e); code != exitOK {
		t.Fatal(code)
	}
	dir := e.lookupEnvValue(config.EnvName("DATA_DIR"))
	db := dbxtest.Open(t, "sqlite://"+filepath.ToSlash(filepath.Join(dir, brand.CLILong+".db")))
	if _, err := db.ExecContext(context.Background(), `INSERT INTO schema_migrations (namespace, version, name, checksum, applied_at) VALUES ('core', 999, '0999_future.sql', 'x', 0)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if code := runBounded(e, "serve"); code != exitFailure || !strings.Contains(e.stdout.(*syncBuffer).String(), dbx.ErrNewerSchema.Error()) {
		t.Errorf("exit code %d\n%s", code, e.stdout)
	}
}

func TestHealthcheckCommand(t *testing.T) {
	e, _, _ := testEnv(t, nil)
	srv := startServe(t, e)
	check, stdout, stderr := testEnv(t, map[string]string{config.EnvName("LISTEN"): srv.addr})
	if code := run(context.Background(), []string{"healthcheck"}, check); code != exitOK {
		t.Errorf("healthcheck: exit code %d: %s", code, stderr)
	}
	if code := run(context.Background(), []string{"healthcheck", "--ready"}, check); code != exitOK || !strings.Contains(stdout.String(), "/readyz") {
		t.Errorf("healthcheck --ready: exit code %d: %s %s", code, stdout, stderr)
	}
	srv.wait(t)
	if code := run(context.Background(), []string{"healthcheck"}, check); code != exitFailure {
		t.Errorf("healthcheck with the server stopped: exit code %d", code)
	}
}

// TestServesTLSWithACertificate: with a certificate the server may listen
// on every interface.
func TestServesTLSWithACertificate(t *testing.T) {
	certFile, keyFile := selfSigned(t)
	e, _, _ := testEnv(t, map[string]string{
		config.EnvName("BASE_URL"):      "https://hub.example.com",
		config.EnvName("LISTEN"):        "0.0.0.0:0",
		config.EnvName("TLS_CERT_FILE"): certFile,
		config.EnvName("TLS_KEY_FILE"):  keyFile,
	})
	srv := startServe(t, e)
	_, port, _ := net.SplitHostPort(srv.addr)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := client.Get("https://127.0.0.1:" + port + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.TLS == nil {
		t.Errorf("%s, TLS %v", resp.Status, resp.TLS != nil)
	}
	check, _, stderr := testEnv(t, map[string]string{
		config.EnvName("BASE_URL"): "https://hub.example.com", config.EnvName("LISTEN"): "0.0.0.0:" + port,
		config.EnvName("TLS_CERT_FILE"): certFile, config.EnvName("TLS_KEY_FILE"): keyFile,
	})
	if code := run(context.Background(), []string{"healthcheck"}, check); code != exitOK {
		t.Errorf("healthcheck over TLS: %d %s", code, stderr)
	}
}

func TestSecretsDoNotReachTheLog(t *testing.T) {
	const marker = "Zq8marker4Kd9setupToken0x7"
	e, _, _ := testEnv(t, map[string]string{config.EnvName("SETUP_TOKEN"): marker, config.EnvName("LOG_LEVEL"): "debug"})
	srv := startServe(t, e)
	get(t, "http://"+srv.addr+"/readyz")
	srv.wait(t)
	if strings.Contains(srv.stdout.String()+srv.stderr.String(), marker) {
		t.Error("the setup token reached the output")
	}
}

func selfSigned(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "hub.example.com"},
		DNSNames:     []string{"hub.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

type recorder struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(b)
}
func (r *recorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}

func mustRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
