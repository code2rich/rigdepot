// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build !unix

package gitrepo

import "os/exec"

// detach does nothing here: prompts are kept away by the environment alone (GIT_TERMINAL_PROMPT,
// GIT_ASKPASS, SSH_ASKPASS_REQUIRE, GCM_INTERACTIVE). Cancelling kills git itself.
func detach(*exec.Cmd) {}
