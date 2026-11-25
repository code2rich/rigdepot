// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package dangerous is the check rule for dangerous commands in a resource. Must fix.
//
// A skill is instructions an AI follows, and its scripts run on people's computers. This rule
// looks at every line of every text file for commands that must never be run that way: deleting
// the root or the home directory, piping a download into a shell, making files writable by
// everyone, switching off TLS certificate checks, and appending to shell startup files. The
// patterns are deliberately narrow: "rm -rf ./build" and "source ~/.zshrc" are fine.
//
// Message keys, all under check.dangerous., without parameters:
//
//	delete_root      rm -rf on /, ~ or $HOME
//	pipe_to_shell    curl or wget piped into a shell, or a shell running what they download
//	world_writable   chmod 777, a+rwx or o+w
//	tls_disabled     curl -k, --no-check-certificate, NODE_TLS_REJECT_UNAUTHORIZED=0 and the like
//	shell_startup    writing to ~/.bashrc, ~/.zshrc, ~/.profile or the PowerShell profile
package dangerous
