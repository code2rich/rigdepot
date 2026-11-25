// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/buildinfo"
)

// Main runs the command-line program with the given arguments (without the
// program name) and returns its exit code.
func Main(args []string) int {
	return run(args, os.Stdout, os.Stderr)
}

// run is the whole program for now: it reports its version. The command tree
// replaces it.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Fprintln(stdout, buildinfo.Get().String(brand.CLI))
		return 0
	}
	fmt.Fprintf(stderr, "%s: no commands are implemented yet; this build only answers --version\n", brand.CLI)
	return 2
}
