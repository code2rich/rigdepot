// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"errors"
	"os"
	"os/exec"
)

// runReleaseSnapshot builds every release artifact into dist/ with the
// pinned goreleaser, without tagging or publishing anything. The first run
// downloads and compiles goreleaser, which needs the network and a while.
func runReleaseSnapshot(root string, args []string) error {
	if len(args) != 0 {
		return errors.New("release-snapshot takes no arguments")
	}
	cmd := exec.Command("go", goTool(releaseModfile, "goreleaser", "release", "--snapshot", "--clean")...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
