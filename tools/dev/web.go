// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The web console (web/) is TypeScript built with Vite; its tools come from
// web/package.json and pnpm-lock.yaml. Only people who change web/, releases
// and browser tests need Node and pnpm: without them check lists the console's
// step as not run, and the Go parts build with a stub page in place of the
// console (web/embed_stub.go).

// webToolEnv keeps pnpm from asking the registry whether it is up to date:
// checks and tests do not use the network.
var webToolEnv = []string{"npm_config_update_notifier=false"}

// webMarkers are the placeholders in the built index.html that the server
// fills in (internal/server/webui). A build without them would serve a page
// with the wrong base path, or one its own content security policy blocks.
var webMarkers = []string{"__BASE_HREF__", "__LANG__", "__CSP_NONCE__"}

// webToolchain reports why the console's checks cannot run on this machine,
// as errNotRun, or nil when they can.
func webToolchain(root string, needModules bool) error {
	if !hasWebConsole(root) {
		return errNotRun{"there is no " + webPackageFile}
	}
	if _, err := exec.LookPath("node"); err != nil {
		return errNotRun{"web console: no Node on PATH"}
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		return errNotRun{"web console: no pnpm on PATH"}
	}
	if needModules {
		if info, err := os.Stat(filepath.Join(root, webDir, "node_modules")); err != nil || !info.IsDir() {
			return errNotRun{"web console: web/node_modules is missing; run first: go run ./tools/dev web-deps"}
		}
	}
	return nil
}

// pnpm runs pnpm on web/ with the given environment (nil: this process's).
func pnpm(root string, env []string, args ...string) *exec.Cmd {
	cmd := exec.Command("pnpm", append([]string{"--dir", webDir}, args...)...)
	cmd.Dir = root
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(append([]string{}, env...), webToolEnv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// stepWeb is the "web console" step of check: format, lint, type check, unit
// tests and a build, the same commands as docs/code-standards.md lists. The
// build goes to the temporary home, so check leaves web/dist alone.
func stepWeb(s *sandbox) (string, error) {
	if err := webToolchain(s.root, true); err != nil {
		return "", err
	}
	out := filepath.Join(s.home, "web-dist")
	steps := []struct {
		name string
		args []string
	}{
		{"prettier", []string{"exec", "prettier", "--check", "."}},
		{"eslint", []string{"exec", "eslint", "."}},
		{"tsc", []string{"exec", "tsc", "--noEmit"}},
		{"vitest", []string{"exec", "vitest", "run"}},
		{"build", []string{"exec", "vite", "build", "--outDir", out, "--emptyOutDir"}},
	}
	var ran []string
	for _, st := range steps {
		fmt.Printf("--> pnpm --dir web %s\n", strings.Join(st.args, " "))
		if err := pnpm(s.root, s.env, st.args...).Run(); err != nil {
			return "", fmt.Errorf("%s: %w", st.name, err)
		}
		ran = append(ran, st.name)
	}
	index, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		return "", fmt.Errorf("the build has no index.html: %w", err)
	}
	for _, m := range webMarkers {
		if !bytes.Contains(index, []byte(m)) {
			return "", fmt.Errorf("the built index.html lacks %s, which the server fills in; keep it in web/index.html", m)
		}
	}
	// The licenses of what the build bundles; release-files writes the same into the notices.
	notices, err := webNotices(s.root)
	if err != nil {
		return "", err
	}
	return strings.Join(ran, ", ") + "; built index.html has the server's markers; " +
		fmt.Sprintf("licenses of the %d bundled packages and fonts allowed", strings.Count(string(notices), "\nLicense: ")), nil
}

// runWebDeps installs the console's dependencies from the lock file. Like
// deps, it is a separate step because it needs the network.
func runWebDeps(root string, args []string) error {
	if len(args) != 0 {
		return errors.New("web-deps takes no arguments")
	}
	if err := webToolchain(root, false); err != nil {
		return err
	}
	fmt.Println("==> pnpm --dir web install --frozen-lockfile")
	return pnpm(root, nil, "install", "--frozen-lockfile").Run()
}

// runWeb builds the console into web/dist, ready to be embedded with
// go build -tags webdist.
func runWeb(root string, args []string) error {
	if len(args) != 0 {
		return errors.New("web takes no arguments")
	}
	if err := webToolchain(root, true); err != nil {
		return err
	}
	fmt.Println("==> pnpm --dir web build")
	if err := pnpm(root, nil, "build").Run(); err != nil {
		return err
	}
	if err := writeWebNotices(root); err != nil {
		return err
	}
	fmt.Printf("wrote web/dist/%s\n", webNoticesFile)
	fmt.Println("\nweb/dist is ready. Build the server with the console in it:\n  go build -tags webdist ./cmd/...")
	return nil
}
