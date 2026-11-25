// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/code2rich/rigdepot/internal/brand"
)

// HTTPS on the server's own listener (docs/architecture/08-deployment.md#https), in the two
// ways it can do it itself; the recommended third, a reverse proxy, needs nothing here.
//
//   - Certificate files (tls.cert_file, tls.key_file): loaded at start, and loaded again on
//     SIGHUP and when the files change (checked every certCheckEvery), so that a renewed
//     certificate is served without a restart. A pair that does not load (half written, the
//     key not matching) is logged and the certificate served so far stays.
//   - Automatic certificates (tls.acme_domains): golang.org/x/crypto/acme/autocert, with the
//     TLS-ALPN-01 challenge on this listener (and HTTP-01 on the redirect listener when there
//     is one), certificates and the account key cached in <data dir>/acme. autocert renews them
//     itself before they expire.

// certCheckEvery is how often the certificate files are checked for a change.
const certCheckEvery = time.Minute

// acmeCacheDir is the directory in the data directory that keeps automatic certificates.
const acmeCacheDir = "acme"

// tlsSetup is how the listener serves TLS.
type tlsSetup struct {
	config *tls.Config
	// redirect wraps the plain HTTP redirect handler: with ACME it also answers HTTP-01
	// challenges. Nil leaves it as it is.
	redirect func(http.Handler) http.Handler
	// watch runs until ctx ends, reloading what needs reloading; nil when nothing does.
	watch func(ctx context.Context)
}

// tlsSetup prepares TLS from the configuration. Certificate files must load now: a server that
// cannot serve its certificate does not start.
func (a *App) tlsSetup() (*tlsSetup, error) {
	if len(a.cfg.TLS.ACMEDomains) > 0 {
		return a.acmeSetup()
	}
	files, err := newCertFiles(a.cfg.TLS.CertFile, a.cfg.TLS.KeyFile, a.log)
	if err != nil {
		return nil, err
	}
	every := a.certCheckEvery
	if every == 0 {
		every = certCheckEvery
	}
	return &tlsSetup{
		config: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: files.getCertificate,
			NextProtos:     []string{"h2", "http/1.1"},
		},
		watch: func(ctx context.Context) { files.watch(ctx, every, a.hangup) },
	}, nil
}

// acmeSetup is tlsSetup for automatic certificates.
func (a *App) acmeSetup() (*tlsSetup, error) {
	cache := filepath.Join(a.cfg.DataDir, acmeCacheDir)
	// DirCache makes the directory on first write; making it now finds a data directory
	// the server cannot write to before the first handshake does.
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return nil, fmt.Errorf("the cache of automatic certificates: %w", err)
	}
	domains := a.cfg.TLS.ACMEDomains
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cache),
		HostPolicy: autocert.HostWhitelist(domains...),
		Email:      a.cfg.TLS.ACMEEmail,
		Client:     &acme.Client{DirectoryURL: a.cfg.TLS.ACMEDirectoryURL, UserAgent: brand.Server},
	}
	cfg := m.TLSConfig()
	cfg.MinVersion = tls.VersionTLS12
	// A client that names no host (a health check by address, curl https://127.0.0.1) gets the
	// certificate of the public address instead of a failed handshake.
	fallback := strings.ToLower(a.cfg.BaseURL.Hostname())
	get := m.GetCertificate
	cfg.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if hello.ServerName == "" {
			h := *hello
			h.ServerName = fallback
			hello = &h
		}
		cert, err := get(hello)
		if err != nil {
			a.log.Warn("no certificate for a TLS handshake", "server_name", hello.ServerName, "err", err)
		}
		return cert, err
	}
	a.log.Info("certificates are issued automatically", "domains", strings.Join(domains, ","),
		"directory", a.cfg.TLS.ACMEDirectoryURL, "cache", cache)
	return &tlsSetup{config: cfg, redirect: m.HTTPHandler}, nil
}

// certFiles serves a certificate from files and loads it again when asked to or when the files
// change.
type certFiles struct {
	certFile, keyFile string
	log               *slog.Logger

	mu   sync.RWMutex
	cert *tls.Certificate
	// tried is what the files looked like at the last load, successful or not, so that a pair
	// that fails to load is not tried (and logged) again until it changes again.
	tried string
}

func newCertFiles(certFile, keyFile string, log *slog.Logger) (*certFiles, error) {
	f := &certFiles{certFile: certFile, keyFile: keyFile, log: log}
	f.tried = f.stamp()
	cert, err := f.load()
	if err != nil {
		return nil, err
	}
	f.cert = cert
	f.logLoaded("TLS certificate loaded", cert)
	return f, nil
}

func (f *certFiles) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.cert, nil
}

func (f *certFiles) load() (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(f.certFile, f.keyFile)
	if err != nil {
		return nil, fmt.Errorf("load the TLS certificate: %w", err)
	}
	if cert.Leaf == nil {
		if cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return nil, fmt.Errorf("load the TLS certificate: %w", err)
		}
	}
	return &cert, nil
}

// stamp describes the two files as they are now: where they lead (renewal tools switch
// symbolic links), their size and modification time. A file that cannot be read stamps as
// such, so that it is tried again once it can be.
func (f *certFiles) stamp() string {
	var b strings.Builder
	for _, name := range []string{f.certFile, f.keyFile} {
		target, err := filepath.EvalSymlinks(name)
		if err != nil {
			fmt.Fprintf(&b, "%s: %v;", name, err)
			continue
		}
		info, err := os.Stat(target)
		if err != nil {
			fmt.Fprintf(&b, "%s: %v;", name, err)
			continue
		}
		fmt.Fprintf(&b, "%s %d %d;", target, info.Size(), info.ModTime().UnixNano())
	}
	return b.String()
}

// reload loads the files again. On failure the certificate served so far stays.
func (f *certFiles) reload(reason string) error {
	stamp := f.stamp()
	cert, err := f.load()
	f.mu.Lock()
	f.tried = stamp
	if err == nil {
		f.cert = cert
	}
	f.mu.Unlock()
	if err != nil {
		f.log.Error("the TLS certificate was not reloaded; the one loaded before is still served", "reason", reason, "err", err)
		return err
	}
	f.logLoaded("TLS certificate reloaded", cert, "reason", reason)
	return nil
}

// reloadIfChanged reloads the files when they changed since they were last loaded.
func (f *certFiles) reloadIfChanged() {
	f.mu.RLock()
	same := f.tried == f.stamp()
	f.mu.RUnlock()
	if !same {
		f.reload("the files changed")
	}
}

// watch reloads on every signal on hangup (SIGHUP) and when the files change, until ctx ends.
func (f *certFiles) watch(ctx context.Context, every time.Duration, hangup <-chan os.Signal) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hangup:
			f.reload("SIGHUP")
		case <-t.C:
			f.reloadIfChanged()
		}
	}
}

// certExpiresSoon is when a loaded certificate's end is worth a warning.
const certExpiresSoon = 14 * 24 * time.Hour

func (f *certFiles) logLoaded(msg string, cert *tls.Certificate, attrs ...any) {
	leaf := cert.Leaf
	attrs = append(attrs, "subject", leaf.Subject.CommonName, "names", strings.Join(leaf.DNSNames, ","),
		"not_after", leaf.NotAfter.UTC().Format(time.RFC3339))
	left := time.Until(leaf.NotAfter)
	switch {
	case left <= 0:
		f.log.Error(msg+", but it has expired", attrs...)
	case left < certExpiresSoon:
		f.log.Warn(msg+", but it expires soon", attrs...)
	default:
		f.log.Info(msg, attrs...)
	}
}
