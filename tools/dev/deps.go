// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// runDeps downloads the modules of the main module and of the pinned
// development tools. It is the only Go step that needs the network; check and
// test run with GOPROXY=off.
func runDeps(root string, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("deps takes no arguments")
	}
	steps := [][]string{
		{"mod", "download"},
		{"mod", "download", "-modfile=" + toolsModfile},
	}
	for _, step := range steps {
		fmt.Printf("==> go %v\n", step)
		cmd := exec.Command("go", step...)
		cmd.Dir = root
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}
