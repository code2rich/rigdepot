// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/config"
)

// writeCert writes a self-signed certificate for hub.example.com with the given serial number
// to certFile and keyFile, and moves their modification time forward so that a change is seen
// even on a file system with coarse timestamps.
func writeCert(t *testing.T, certFile, keyFile string, serial int64, bump time.Duration) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "hub.example.com"}, DNSNames: []string{"hub.example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(30 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(bump)
	for _, f := range []string{certFile, keyFile} {
		if err := os.Chtimes(f, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

// servedSerial connects to addr and returns the serial number of the certificate it serves.
func servedSerial(t *testing.T, addr string) int64 {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: "hub.example.com", InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
}

// waitSerial waits until addr serves the certificate with the given serial number.
func waitSerial(t *testing.T, addr string, want int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := servedSerial(t, addr)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("serves certificate %d, want %d", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func tlsEnv(t *testing.T, certFile, keyFile string) (*env, chan os.Signal) {
	t.Helper()
	e, _, _ := testEnv(t, map[string]string{
		config.EnvName("BASE_URL"):      "https://hub.example.com",
		config.EnvName("LISTEN"):        "127.0.0.1:0",
		config.EnvName("TLS_CERT_FILE"): certFile,
		config.EnvName("TLS_KEY_FILE"):  keyFile,
	})
	hup := make(chan os.Signal, 1)
	e.hangup = hup
	return e, hup
}

// TestCertificateReloadsOnSIGHUP is the acceptance check "a changed certificate is served
// without a restart", by signal: the files change, nothing happens until SIGHUP.
func TestCertificateReloadsOnSIGHUP(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	writeCert(t, certFile, keyFile, 1, 0)
	e, hup := tlsEnv(t, certFile, keyFile)
	e.certCheckEvery = time.Hour // only the signal
	srv := startServe(t, e)
	if got := servedSerial(t, srv.addr); got != 1 {
		t.Fatalf("serves %d", got)
	}
	writeCert(t, certFile, keyFile, 2, time.Second)
	if got := servedSerial(t, srv.addr); got != 1 {
		t.Errorf("reloaded before the signal: %d", got)
	}
	hup <- os.Signal(nil)
	waitSerial(t, srv.addr, 2)
	srv.wait(t)
	if !strings.Contains(srv.stdout.String(), `"msg":"TLS certificate reloaded","reason":"SIGHUP"`) {
		t.Errorf("the reload is not in the log:\n%s", srv.stdout)
	}
}

// TestCertificateReloadsWhenTheFilesChange: without a signal, a change of the files is noticed
// by itself. A pair that does not load (here a key that does not match) leaves the certificate
// served so far in place, and the next good pair is picked up.
func TestCertificateReloadsWhenTheFilesChange(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	writeCert(t, certFile, keyFile, 1, 0)
	e, _ := tlsEnv(t, certFile, keyFile)
	e.certCheckEvery = 20 * time.Millisecond
	srv := startServe(t, e)
	writeCert(t, certFile, keyFile, 2, time.Second)
	waitSerial(t, srv.addr, 2)

	// Half of a renewal: a new certificate next to the old key.
	otherDir := t.TempDir()
	writeCert(t, filepath.Join(otherDir, "cert.pem"), filepath.Join(otherDir, "key.pem"), 3, 0)
	data, err := os.ReadFile(filepath.Join(otherDir, "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	os.Chtimes(certFile, later, later)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(srv.stdout.String(), "the TLS certificate was not reloaded") {
		if time.Now().After(deadline) {
			t.Fatalf("the failed reload is not in the log:\n%s", srv.stdout)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := servedSerial(t, srv.addr); got != 2 {
		t.Errorf("after a pair that does not load: serves %d, want 2 still", got)
	}
	// While the files stay as they are, the pair is not tried (and reported) again.
	reported := strings.Count(srv.stdout.String(), "the TLS certificate was not reloaded")
	time.Sleep(20 * e.certCheckEvery)
	if again := strings.Count(srv.stdout.String(), "the TLS certificate was not reloaded"); again != reported {
		t.Errorf("the unchanged broken pair was reported %d more times", again-reported)
	}

	writeCert(t, certFile, keyFile, 4, 3*time.Second)
	waitSerial(t, srv.addr, 4)
	srv.wait(t)
}

// TestCertificateFilesMustLoadAtStart: a server that cannot serve its certificate does not
// start.
func TestCertificateFilesMustLoadAtStart(t *testing.T) {
	dir := t.TempDir()
	e, _ := tlsEnv(t, filepath.Join(dir, "missing.pem"), filepath.Join(dir, "missing-key.pem"))
	if code := runBounded(e, "serve"); code != exitFailure || !strings.Contains(e.stdout.(*syncBuffer).String(), "load the TLS certificate") {
		t.Errorf("exit %d\n%s", code, e.stdout)
	}
}

// TestAutomaticCertificates is the acceptance check of ACME against a certificate authority on
// this machine: the first handshake for the domain gets a certificate issued (the authority
// checks the TLS-ALPN-01 answer on the server's own port), it is cached in the data directory,
// and a restarted server serves it from the cache without asking again.
func TestAutomaticCertificates(t *testing.T) {
	ca := newFakeACME(t)
	vars := map[string]string{
		config.EnvName("BASE_URL"):               "https://hub.example.com",
		config.EnvName("LISTEN"):                 "127.0.0.1:0",
		config.EnvName("TLS_ACME_DOMAINS"):       "hub.example.com",
		config.EnvName("TLS_ACME_EMAIL"):         "ops@example.com",
		config.EnvName("TLS_ACME_DIRECTORY_URL"): ca.directory(),
	}
	e, _, _ := testEnv(t, vars)
	dataDir := e.lookupEnvValue(config.EnvName("DATA_DIR"))
	vars[config.EnvName("DATA_DIR")] = dataDir
	srv := startServe(t, e)
	ca.setTarget(srv.addr)

	client := func(addr string) *http.Client {
		return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{ServerName: "hub.example.com", RootCAs: ca.roots()},
		}}
	}
	resp, err := client(srv.addr).Get("https://" + srv.addr + "/healthz")
	if err != nil {
		t.Fatalf("%v\n%s", err, srv.stdout)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.TLS.PeerCertificates[0].Issuer.CommonName != "Fake ACME root" {
		t.Fatalf("%s, issuer %s", resp.Status, resp.TLS.PeerCertificates[0].Issuer)
	}
	if accounts, issued := ca.counts(); accounts != 1 || issued != 1 {
		t.Errorf("accounts %d, certificates %d; want 1 and 1", accounts, issued)
	}
	if _, err := os.Stat(filepath.Join(dataDir, acmeCacheDir, "hub.example.com")); err != nil {
		t.Errorf("the certificate is not cached: %v", err)
	}

	// A client that names no host gets the certificate of the public address.
	conn, err := tls.Dial("tcp", srv.addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if names := conn.ConnectionState().PeerCertificates[0].DNSNames; len(names) != 1 || names[0] != "hub.example.com" {
		t.Errorf("without a server name: %v", names)
	}
	conn.Close()
	// Other names get nothing.
	if _, err := tls.Dial("tcp", srv.addr, &tls.Config{ServerName: "other.example.com", InsecureSkipVerify: true}); err == nil {
		t.Error("a handshake for a name not in tls.acme_domains succeeded")
	}
	srv.wait(t)

	// Restart on the same data directory: served from the cache.
	e2, _, _ := testEnv(t, vars)
	srv = startServe(t, e2)
	resp, err = client(srv.addr).Get("https://" + srv.addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, issued := ca.counts(); issued != 1 {
		t.Errorf("the restarted server asked for another certificate (%d issued)", issued)
	}
	_, port, _ := net.SplitHostPort(srv.addr)
	check, _, stderr := testEnv(t, map[string]string{
		config.EnvName("BASE_URL"): "https://hub.example.com", config.EnvName("LISTEN"): "127.0.0.1:" + port,
		config.EnvName("TLS_ACME_DOMAINS"): "hub.example.com",
	})
	if code := run(context.Background(), []string{"healthcheck"}, check); code != exitOK {
		t.Errorf("healthcheck with automatic certificates: %d %s", code, stderr)
	}
}
