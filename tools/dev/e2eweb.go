// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// e2e-web runs the console's browser tests (web/e2e, Playwright in Chromium
// and WebKit) twice:
//
//   - mock: every page against the development stand-in for the server
//     (VITE_API_MOCK=1), which Playwright starts on a free port: three roles,
//     light and dark, 390 and 1440 px, Chinese, English and the
//     pseudo-language, the keyboard through drawers and dialogs, and axe.
//   - real: the console built into a real server on a loopback port with an
//     empty SQLite database in the temporary home; the tests go through the
//     first-run wizard and the main paths.
//
// It is not part of check: it downloads nothing, but it needs the browsers
// (go run ./tools/dev web-deps --browsers, which uses the network) and takes
// minutes.

// e2eSetupToken is the first-run token the real server is started with, so
// the wizard test can type it.
const e2eSetupToken = "<placeholder: setup token>"

type e2eOptions struct {
	only  string // "", "mock" or "real"
	out   string // screenshots, reports, traces, the server's log
	shots bool
	extra []string // passed on to playwright test
}

func parseE2EArgs(root string, args []string) (e2eOptions, error) {
	o := e2eOptions{out: filepath.Join(root, ".dev", "e2e-web")}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "mock" || a == "real":
			o.only = a
		case a == "--shots":
			o.shots = true
		case a == "--out" && i+1 < len(args):
			i++
			o.out = args[i]
		case strings.HasPrefix(a, "--out="):
			o.out = strings.TrimPrefix(a, "--out=")
		case a == "--":
			o.extra = args[i+1:]
			i = len(args)
		default:
			return o, fmt.Errorf("unknown argument %q\nusage: e2e-web [mock|real] [--shots] [--out DIR] [-- playwright test arguments]", a)
		}
	}
	out, err := filepath.Abs(o.out)
	o.out = out
	return o, err
}

func runE2EWeb(root string, args []string) error {
	o, err := parseE2EArgs(root, args)
	if err != nil {
		return err
	}
	if err := webToolchain(root, true); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, webDir, "node_modules", "@playwright", "test")); err != nil {
		return errors.New("the browser tests are not installed (web/node_modules/@playwright/test); run first: go run ./tools/dev web-deps --browsers")
	}
	// The browsers stay where Playwright keeps them for this user; the
	// tests themselves run with a temporary home.
	browsers := os.Getenv("PLAYWRIGHT_BROWSERS_PATH")
	if browsers == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		browsers = filepath.Join(cache, "ms-playwright")
	}
	s, err := newSandbox(root)
	if err != nil {
		return err
	}
	defer s.close()
	if err := os.MkdirAll(o.out, 0o755); err != nil {
		return err
	}
	shots := "0"
	if o.shots {
		shots = "1"
	}
	env := mergeEnv(s.env, map[string]string{
		"PLAYWRIGHT_BROWSERS_PATH": browsers,
		"E2E_OUT":                  o.out,
		"E2E_SHOTS":                shots,
	})

	var failed []string
	if o.only != "real" {
		if err := e2eMock(s, env, o); err != nil {
			failed = append(failed, "mock: "+err.Error())
		}
	}
	if o.only != "mock" {
		if err := e2eReal(s, env, o); err != nil {
			failed = append(failed, "real: "+err.Error())
		}
	}
	fmt.Printf("\nreports: %s/{mock,real}/report/index.html\n", o.out)
	if o.shots {
		fmt.Printf("screenshots: %s/shots\n", o.out)
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

func e2eMock(s *sandbox, env []string, o e2eOptions) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	fmt.Println("==> browser tests against the development stand-in (VITE_API_MOCK=1)")
	env = mergeEnv(env, map[string]string{"E2E_MODE": "mock", "E2E_PORT": port})
	return pnpm(s.root, env, append([]string{"exec", "playwright", "test"}, o.extra...)...).Run()
}

func e2eReal(s *sandbox, env []string, o e2eOptions) error {
	b, err := readBrand(s.root)
	if err != nil {
		return err
	}
	// The server embeds web/dist (go build -tags webdist), so the console is
	// built there, as go run ./tools/dev web does.
	fmt.Println("==> pnpm --dir web build")
	if err := pnpm(s.root, mergeEnv(s.env, map[string]string{"VITE_API_MOCK": ""}), "build").Run(); err != nil {
		return err
	}
	exe := filepath.Join(s.home, "bin", b.Server)
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	fmt.Println("==> go build -tags webdist ./cmd/" + b.Server)
	if err := s.command(nil, "go", "build", "-tags", "webdist", "-o", exe, "./cmd/"+b.Server).Run(); err != nil {
		return err
	}
	port, err := freePort()
	if err != nil {
		return err
	}
	addr := "127.0.0.1:" + port
	url := "http://" + addr
	logDir := filepath.Join(o.out, "real")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	logFile, err := os.Create(filepath.Join(logDir, "server.log"))
	if err != nil {
		return err
	}
	defer logFile.Close()
	srv := exec.Command(exe, "serve")
	srv.Dir = s.home
	srv.Env = mergeEnv(s.env, map[string]string{
		b.EnvPrefix + "BASE_URL":    url,
		b.EnvPrefix + "LISTEN":      addr,
		b.EnvPrefix + "DATA_DIR":    filepath.Join(s.home, "server"),
		b.EnvPrefix + "SETUP_TOKEN": e2eSetupToken,
	})
	srv.Stdout, srv.Stderr = logFile, logFile
	fmt.Printf("==> %s on %s, SQLite in the temporary home (log: %s)\n", b.Server, url, logFile.Name())
	if err := srv.Start(); err != nil {
		return err
	}
	defer func() {
		if runtime.GOOS == "windows" {
			srv.Process.Kill()
		} else {
			srv.Process.Signal(os.Interrupt)
		}
		srv.Wait()
	}()
	if err := waitReady(url+"/readyz", 30*time.Second); err != nil {
		return fmt.Errorf("%w (see %s)", err, logFile.Name())
	}
	fmt.Println("==> browser tests against the real server")
	env = mergeEnv(env, map[string]string{
		"E2E_MODE":        "real",
		"E2E_BASE_URL":    url,
		"E2E_SETUP_TOKEN": e2eSetupToken,
	})
	return pnpm(s.root, env, append([]string{"exec", "playwright", "test"}, o.extra...)...).Run()
}

// freePort asks the system for a loopback port nobody is using.
func freePort() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	return port, err
}
