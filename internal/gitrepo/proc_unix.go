// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build unix

package gitrepo

import (
	"os/exec"
	"syscall"
)

// detach starts git in a session of its own. Without a controlling terminal, ssh cannot ask for
// a passphrase or a host key confirmation, and git's own prompts have nowhere to go. Cancelling
// kills the whole process group, so that helpers git started (ssh, git-remote-https) do not
// outlive it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
