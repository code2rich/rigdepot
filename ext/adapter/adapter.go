// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adapter

import (
	"context"
	"io/fs"
)

// Adapter describes one AI coding tool. An implementation is stateless: the same input gives the
// same output, and nothing is remembered between calls.
type Adapter interface {
	// ID returns the stable identifier of the tool, in lower case with hyphens, such as
	// "claude-code". It is recorded in the manifest and cannot change after a release.
	ID() string

	// Profile returns the declarative part of the adapter: display name, how the tool is
	// detected, where each kind of artifact goes, which other tools' directories it reads, and
	// the tool version all of this was verified against.
	Profile() Profile

	// Detect looks, without changing anything, whether the tool is installed, which version it
	// is and which configuration roots it really uses.
	//
	// When something cannot be read (no permission, a directory that is there but looks wrong),
	// the status is Unknown, not NotInstalled: failing to read means not knowing. A non-nil
	// error is for a failure of the detection itself, such as a cancelled context; the core
	// treats the tool as Unknown then as well.
	Detect(ctx context.Context, env Env) (Detection, error)

	// Capabilities says which artifacts this version of the tool takes. The core uses it to
	// decide between delivering a resource natively and falling back to another form. For a
	// profile without a verification record the answer is always the zero value: nothing is
	// delivered to a tool nobody has checked.
	Capabilities(d Detection) Capabilities

	// Plan translates artifacts into operations. It only computes: it must not write a file,
	// run a command or use the network.
	//
	// When an existing configuration file cannot be parsed, Plan returns no operation on that
	// file and explains why in Plan.Skips. An unreadable file is never treated as an empty one.
	// A non-nil error means the plan as a whole could not be computed, and the core then writes
	// nothing for this tool in this run.
	Plan(ctx context.Context, req PlanRequest) (Plan, error)
}

// AutoApprover is an optional interface of an Adapter. It tells whether the tool is currently
// set to approve every action without asking, which matters when the product has to guarantee
// that a person confirmed something.
type AutoApprover interface {
	// AutoApprove returns TriUnknown when it cannot tell, for example because the mode was
	// given on the tool's command line. The core treats TriUnknown as "no guarantee".
	AutoApprove(ctx context.Context, env Env, d Detection) Tri
}

// Tri is an answer that may be unknown. The zero value is TriUnknown, so an answer nobody filled
// in is never mistaken for a no.
type Tri int

const (
	TriUnknown Tri = iota // could not be determined
	TriNo
	TriYes
)

// String returns "unknown", "no" or "yes".
func (t Tri) String() string {
	switch t {
	case TriNo:
		return "no"
	case TriYes:
		return "yes"
	}
	return "unknown"
}

// Env is everything an adapter can see of the computer it runs on. It is read-only. During a
// preview Home points at a temporary copy, and ReadFile and Stat read from that copy; an adapter
// that goes around Env would describe the wrong directory.
//
// The core fills in every field.
type Env struct {
	// GOOS is the operating system the tool runs on, as in runtime.GOOS. Take platform
	// differences from here, so that they can be tested on any platform.
	GOOS string

	// Home is the absolute path of the user's home directory in the form the operating system
	// uses. Adapters rarely need it: paths inside it are written "~/…".
	Home string

	// Getenv returns the value of an environment variable, or "". Use it only for the tool's
	// own variables, such as the one that moves its configuration root.
	Getenv func(key string) string

	// ReadFile returns the content of a file. The path is written as described in the package
	// documentation ("~/…" or absolute) and must lie inside Home or one of the roots the
	// adapter's profile declares; anything else is refused. A file that does not exist is
	// reported with an error that satisfies errors.Is(err, fs.ErrNotExist), so that "missing"
	// and "unreadable" can be told apart.
	ReadFile func(path string) ([]byte, error)

	// Stat describes a file or directory, under the same rules as ReadFile.
	Stat func(path string) (fs.FileInfo, error)

	// LookPath finds the tool's executable by name, the way a shell would.
	LookPath func(name string) (string, error)

	// Version runs the executable with the given arguments, with a timeout, and returns what
	// it printed. It exists for "<tool> --version" and must not be used for anything that
	// changes state.
	Version func(ctx context.Context, bin string, args ...string) (string, error)
}

// Status says whether a tool is installed. The zero value is Unknown.
type Status int

const (
	// Unknown means the adapter could not tell, because something it needed could not be read.
	// The tool is left alone for this run: nothing is written, nothing is removed.
	Unknown Status = iota
	// NotInstalled means the adapter looked and the tool is not there.
	NotInstalled
	// Installed means the tool is there and its configuration roots are known.
	Installed
)

// String returns "unknown", "not_installed" or "installed", the names used in machine-readable
// output.
func (s Status) String() string {
	switch s {
	case NotInstalled:
		return "not_installed"
	case Installed:
		return "installed"
	}
	return "unknown"
}

// Detection is what Detect found out about a tool on this computer.
type Detection struct {
	Status Status

	// Version is the version the tool reports about itself, or "" when it could not be had.
	Version string

	// Roots maps the names a profile gives its roots ("config", "skills") to where they are on
	// this computer, with environment variables already applied and written as described in
	// the package documentation ("~/.claude", or absolute when outside the home directory).
	Roots map[string]string

	// Reason is the message key of the explanation shown to the person when Status is Unknown.
	Reason string
}

// Capabilities lists which artifacts a tool takes. The zero value takes nothing.
type Capabilities struct {
	Skills   bool // skill directories
	Commands bool // slash commands
	Agents   bool // sub-agents
	Hooks    bool // declarative hooks
	Plugin   bool // a stable plugin mechanism of its own, with a command line to drive it
	MCP      MCPCaps
}

// MCPCaps lists what a tool supports for MCP servers.
type MCPCaps struct {
	Stdio bool // servers started as a local process
	HTTP  bool // servers reached over HTTP
	// EnvRef means the tool's configuration can say "take this value from an environment
	// variable", so a secret does not have to appear in the file.
	EnvRef bool
	// ToolSearch means the tool loads tool definitions on demand by itself.
	ToolSearch bool
}
