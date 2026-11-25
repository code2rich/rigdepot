// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package scripts embeds the install scripts. They are published with every
// release, and rgd-server hands out a copy of each with its own address,
// version and checksums filled in (GET /install.sh, GET /install.ps1), so
// that there is one script to read and review, not two.
package scripts

import _ "embed"

// InstallSh is install.sh, the installer for macOS and Linux.
//
//go:embed install.sh
var InstallSh string

// InstallPs1 is install.ps1, the installer for Windows.
//
//go:embed install.ps1
var InstallPs1 string
