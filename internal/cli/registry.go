// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// Adding a command is adding one file to this package:
//
//	// status.go
//	func init() { register("", newStatusCmd) }
//
//	func newStatusCmd(a *app) *cobra.Command {
//		return &cobra.Command{
//			Use:   "status [id]",
//			Short: a.p.T(msg.StatusShort),
//			Args:  a.maxArgs(1),
//			RunE: func(cmd *cobra.Command, args []string) error {
//				...
//				return a.out.Result(cliout.Schema("status", 1), result, func(w io.Writer) error { ... })
//			},
//		}
//	}
//
// The messages go into locales/en/cli.json and locales/zh-CN/cli.json, then
// "go run ./tools/dev gen" makes the msg constants. A subcommand names its
// parent: register("mcp", newMCPRunCmd) hangs "run" below "mcp", whose own
// file registers it with register("", newMCPCmd) and builds it with
// a.group. Nothing else needs to change: the tree is assembled from what is
// registered.

// builder makes one command for one run of the program.
type builder func(a *app) *cobra.Command

type registration struct {
	parent string // the parent's path below the root, such as "mcp"; "" for a top-level command
	build  builder
}

var registrations []registration

// register adds a command to the tree. It is called from init in the
// command's own file.
func register(parent string, b builder) {
	registrations = append(registrations, registration{parent: parent, build: b})
}

// attach builds every registered command and hangs it below its parent.
// Parents are attached before their children whatever the order of
// registration. A parent that does not exist, or two commands with one name,
// is a programming error and panics: a test catches it at once.
func attach(a *app, root *cobra.Command) {
	regs := append([]registration(nil), registrations...)
	depth := func(parent string) int { return len(strings.Fields(parent)) }
	sort.SliceStable(regs, func(i, j int) bool { return depth(regs[i].parent) < depth(regs[j].parent) })
	byPath := map[string]*cobra.Command{"": root}
	for _, r := range regs {
		parent, ok := byPath[r.parent]
		if !ok {
			panic(fmt.Sprintf("cli: command registered below %q, which is not a command", r.parent))
		}
		cmd := r.build(a)
		path := strings.TrimSpace(r.parent + " " + cmd.Name())
		if _, dup := byPath[path]; dup {
			panic(fmt.Sprintf("cli: two commands named %q", path))
		}
		byPath[path] = cmd
		parent.AddCommand(cmd)
	}
}
