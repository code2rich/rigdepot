// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
)

// runHealthcheck asks the server on this machine whether it is alive
// (/healthz) or, with --ready, ready (/readyz). It exists so that the
// container image does not need curl. It reads the same configuration as
// serve to find the address.
func runHealthcheck(ctx context.Context, e *env, args []string) int {
	var ready bool
	cfg, code := loadConfig("healthcheck", e, args, func(fs *flag.FlagSet) {
		fs.BoolVar(&ready, "ready", false, "check readiness (/readyz) instead of liveness (/healthz)")
	})
	if cfg == nil {
		return code
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		fmt.Fprintf(e.stderr, "%s healthcheck: %v\n", brand.Server, err)
		return exitUsage
	}
	// A server listening on every interface is reached through loopback.
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
		if ip != nil && ip.To4() == nil {
			host = "::1"
		}
	}
	scheme := "http"
	client := &http.Client{Timeout: 3 * time.Second}
	if cfg.ServesTLS() {
		scheme = "https"
		// The certificate names the public host, not the loopback address
		// this connects to. Nothing secret is sent and the answer is only
		// a status, so the name is not checked. The public host goes in
		// the handshake all the same: automatic certificates are chosen by it.
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: cfg.BaseURL.Hostname()}}
	}
	path := "/healthz"
	if ready {
		path = "/readyz"
	}
	url := scheme + "://" + net.JoinHostPort(host, port) + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(e.stderr, "%s healthcheck: %v\n", brand.Server, err)
		return exitFailure
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(e.stderr, "%s healthcheck: %v\n", brand.Server, err)
		return exitFailure
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(e.stderr, "%s healthcheck: %s answered %s\n", brand.Server, path, resp.Status)
		return exitFailure
	}
	fmt.Fprintf(e.stdout, "%s: %s\n", path, resp.Status)
	return exitOK
}
