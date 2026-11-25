// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// targets are the six platforms every release is built for. Building for a
// platform proves the code compiles there, not that it works there.
var targets = []struct{ os, arch string }{
	{"darwin", "arm64"}, {"darwin", "amd64"},
	{"linux", "amd64"}, {"linux", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

// coreOnlyTag builds without any optional module.
const coreOnlyTag = "rgd_core_only"

// errNotRun is returned by a step that has nothing to check yet or lacks a
// tool. The reason ends up in the result table: a step that did not run is
// listed as not run, never left out.
type errNotRun struct{ reason string }

func (e errNotRun) Error() string { return e.reason }

type step struct {
	name string
	run  func(s *sandbox) (detail string, err error)
}

type result struct {
	name    string
	status  string // "pass", "FAIL" or "not run"
	detail  string
	elapsed time.Duration
}

func checkSteps() []step {
	return []step{
		{"format", stepFormat},
		{"vet", func(s *sandbox) (string, error) {
			return "go vet ./...", s.command(nil, "go", "vet", "./...").Run()
		}},
		{"staticcheck", stepStaticcheck},
		{"generated files", func(s *sandbox) (string, error) {
			return "brand.json and api/openapi.yaml regenerated and compared", gen(s.root, s.env, true)
		}},
		{"structure rules", stepStructure},
		{"messages", func(s *sandbox) (string, error) {
			if !isDir(filepath.Join(s.root, "locales")) {
				return "", errNotRun{"there is no locales/ directory yet"}
			}
			return "", errors.New("locales/ exists but the message check is not implemented in tools/dev")
		}},
		{"tests", func(s *sandbox) (string, error) {
			return "go test -count=1 ./...", s.command(nil, "go", "test", "-count=1", "./...").Run()
		}},
		{"core only", stepCoreOnly},
		{"cross-compile", stepCrossCompile},
		{"web console", func(s *sandbox) (string, error) {
			if !isFile(filepath.Join(s.root, "web", "package.json")) {
				return "", errNotRun{"there is no web/package.json yet"}
			}
			return "", errors.New("web/package.json exists but the web console checks are not implemented in tools/dev")
		}},
	}
}

func stepFormat(s *sandbox) (string, error) {
	goroot, err := goEnv(s.root, "GOROOT")
	if err != nil {
		return "", err
	}
	out, err := output(s.root, s.env, filepath.Join(goroot, "bin", "gofmt"), "-l", ".")
	if err != nil {
		return "", err
	}
	if out != "" {
		return "", fmt.Errorf("these files are not gofmt-formatted:\n%s", out)
	}
	return "gofmt -l .", nil
}

func stepStaticcheck(s *sandbox) (string, error) {
	if !isFile(filepath.Join(s.root, filepath.FromSlash(toolsModfile))) {
		return "", fmt.Errorf("%s is missing", toolsModfile)
	}
	if err := s.command(nil, "go", goTool(toolsModfile, "staticcheck", "./...")...).Run(); err != nil {
		return "", fmt.Errorf("%w (if the tool could not be built, run first: go run ./tools/dev deps)", err)
	}
	return "version pinned in " + toolsModfile, nil
}

func stepStructure(s *sandbox) (string, error) {
	violations, err := checkStructure(s)
	if err != nil {
		return "", err
	}
	if len(violations) > 0 {
		var b strings.Builder
		for _, v := range violations {
			b.WriteString("  " + v.String() + "\n")
		}
		return "", fmt.Errorf("%d violation(s):\n%s", len(violations), strings.TrimRight(b.String(), "\n"))
	}
	return "R1-R8, registration, test isolation, brand literals, license headers", nil
}

func stepCoreOnly(s *sandbox) (string, error) {
	if err := s.command(nil, "go", "build", "-tags", coreOnlyTag, "./...").Run(); err != nil {
		return "", err
	}
	return "build and test with -tags " + coreOnlyTag,
		s.command(nil, "go", "test", "-count=1", "-tags", coreOnlyTag, "./...").Run()
}

func stepCrossCompile(s *sandbox) (string, error) {
	var built []string
	for _, t := range targets {
		// -o points into the temporary home: without it, a module with a
		// single main package would drop the binary into the working tree.
		out := filepath.Join(s.home, "build", t.os+"_"+t.arch) + string(filepath.Separator)
		if err := os.MkdirAll(out, 0o755); err != nil {
			return "", err
		}
		env := []string{"CGO_ENABLED=0", "GOOS=" + t.os, "GOARCH=" + t.arch}
		if err := s.command(env, "go", "build", "-trimpath", "-o", out, "./...").Run(); err != nil {
			return "", fmt.Errorf("%s/%s: %w", t.os, t.arch, err)
		}
		built = append(built, t.os+"/"+t.arch)
	}
	return "CGO_ENABLED=0: " + strings.Join(built, " "), nil
}

// runCheck runs every pre-merge check in order and stops at the first
// failure. It always ends with a table of what passed, what failed and what
// did not run, ready to paste into a commit message or pull request.
func runCheck(root string, args []string) error {
	if len(args) != 0 {
		return errors.New("check takes no arguments")
	}
	s, err := newSandbox(root)
	if err != nil {
		return err
	}
	var results []result
	failed := false
	for _, st := range checkSteps() {
		if failed {
			results = append(results, result{name: st.name, status: "not run", detail: "an earlier step failed"})
			continue
		}
		fmt.Printf("==> %s\n", st.name)
		start := time.Now()
		detail, err := st.run(s)
		r := result{name: st.name, status: "pass", detail: detail, elapsed: time.Since(start)}
		var skipped errNotRun
		switch {
		case errors.As(err, &skipped):
			r.status, r.detail = "not run", skipped.reason
		case err != nil:
			r.status, r.detail = "FAIL", firstLine(err.Error())
			fmt.Fprintf(os.Stderr, "%s: %v\n", st.name, err)
			failed = true
		}
		results = append(results, r)
	}
	envDetail := "temporary home " + s.home + " (removed); GOFLAGS=-mod=readonly GOPROXY=off"
	envStatus := "pass"
	if err := s.close(); err != nil {
		envStatus, envDetail, failed = "FAIL", "could not remove the temporary home: "+err.Error(), true
	}
	results = append([]result{{name: "environment", status: envStatus, detail: envDetail}}, results...)
	printResults(results)
	if failed {
		return errors.New("failed")
	}
	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func printResults(results []result) {
	goVersion, _ := output("", nil, "go", "env", "GOVERSION", "GOOS", "GOARCH")
	fmt.Printf("\ncheck results (%s)\n\n", strings.Join(strings.Fields(goVersion), " "))
	fmt.Printf("| %-16s | %-7s | %-6s | %s\n", "step", "result", "time", "detail")
	fmt.Printf("| %s | %s | %s | %s\n", strings.Repeat("-", 16), strings.Repeat("-", 7), strings.Repeat("-", 6), strings.Repeat("-", 6))
	for _, r := range results {
		elapsed := ""
		if r.elapsed > 0 {
			elapsed = fmt.Sprintf("%.1fs", r.elapsed.Seconds())
		}
		fmt.Printf("| %-16s | %-7s | %6s | %s\n", r.name, r.status, elapsed, r.detail)
	}
	fmt.Println()
}

// runTest runs go test in the same isolated environment as check. Without
// arguments it tests the whole repository.
func runTest(root string, args []string) error {
	s, err := newSandbox(root)
	if err != nil {
		return err
	}
	defer s.close()
	if len(args) == 0 {
		args = []string{"./..."}
	}
	return s.command(nil, "go", append([]string{"test", "-count=1"}, args...)...).Run()
}
