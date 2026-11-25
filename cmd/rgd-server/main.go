// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Command rgd-server is the self-hosted server for a team: it manages who may
// use what, releases, personal secrets, audit and feedback, and serves the
// embedded web console.
package main

import (
	"os"

	_ "github.com/code2rich/rigdepot/builtin/all"
	_ "github.com/code2rich/rigdepot/builtin/all/server"
	"github.com/code2rich/rigdepot/internal/server"
	_ "github.com/code2rich/rigdepot/modules/all"
)

func main() { os.Exit(server.Main(os.Args[1:])) }
