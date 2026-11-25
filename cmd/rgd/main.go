// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Command rgd is the command-line program that runs on each person's
// computer. It syncs a team's AI coding resources into the AI coding tools
// installed there, records everything it writes and can undo all of it.
package main

import (
	"os"

	_ "github.com/code2rich/rigdepot/builtin/all"
	"github.com/code2rich/rigdepot/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
