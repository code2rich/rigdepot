// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Command dev is the single entry point for every development task in this
// repository. It uses only the standard library, so it runs on macOS, Linux
// and Windows without bash or make:
//
//	go run ./tools/dev <task>
//
// CI runs the same tasks; no check lives in the Makefile or in workflow YAML.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

type task struct {
	summary string
	run     func(root string, args []string) error
}

var tasks = map[string]task{
	"deps":             {"download dependencies (the only step that needs the network)", runDeps},
	"check":            {"run every pre-merge check and print a result table", runCheck},
	"test":             {"run tests in an isolated environment: test [packages] [go test flags]", runTest},
	"gen":              {"regenerate generated files; --check only reports whether they are stale", runGen},
	"check-linux":      {"run check inside a local Linux container (needs Docker)", runCheckLinux},
	"release-snapshot": {"build all release artifacts into dist/ without publishing, twice, and compare", runReleaseSnapshot},
	"release":          {"publish the version tag on HEAD (needs GITHUB_TOKEN); --dry-run builds and checks only", runRelease},
	"release-files":    {"write the completions and third-party notices the release archives carry", runReleaseFiles},
	"web-deps":         {"install the web console's dependencies (needs Node, pnpm and the network)", runWebDeps},
	"web":              {"build the web console into web/dist for go build -tags webdist", runWeb},
	"load":             {"load-test the server in a container limited to 2 CPUs and 2 GB (needs Docker): load [flags]", runLoad},
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	t, ok := tasks[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "dev: unknown task %q\n\n", args[0])
		usage()
		return 2
	}
	root, err := findRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "dev:", err)
		return 1
	}
	if err := t.run(root, args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "dev %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

func usage() {
	names := make([]string, 0, len(tasks))
	for name := range tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("usage: go run ./tools/dev <task> [arguments]\n\ntasks:\n")
	for _, name := range names {
		fmt.Fprintf(&b, "  %-17s %s\n", name, tasks[name].summary)
	}
	fmt.Fprint(os.Stderr, b.String())
}
