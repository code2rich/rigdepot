// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build !unix

package realtools

import "os/exec"

// ownProcessGroup does nothing here: only the server itself is ended. What it started ends when
// its standard input closes.
func ownProcessGroup(*exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
