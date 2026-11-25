// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adapter

import "github.com/code2rich/rigdepot/ext/resource"

// PlanRequest is the input of Adapter.Plan.
type PlanRequest struct {
	// Env is how the adapter reads the computer. During a preview it reads a temporary copy.
	Env Env
	// Detection is what Detect returned for the same Env.
	Detection Detection
	// Artifacts is everything that should be in the tool after the run, already rendered in
	// forms the tool's Capabilities allow.
	Artifacts []Artifact
	Options   Options
}

// Options are the choices the core made for a plan.
type Options struct {
	// MCPMode says how MCP servers that need personal secrets are delivered.
	MCPMode MCPMode
}

// MCPMode is the way MCP servers are written into a tool. A secret's value must never end up in
// a configuration file, which leaves three ways.
type MCPMode string

const (
	// MCPNativeEnv writes one entry per server and expresses each secret in the tool's own
	// syntax for "take this from an environment variable"; the person sets the variable. It
	// needs MCPCaps.EnvRef; without it, a server that needs a secret is reported in Plan.Skips
	// with NeedsSecretSupport. This is the only mode that works without a server.
	MCPNativeEnv MCPMode = "native-env"
	// MCPLauncher writes one entry per server that starts the server through the product's
	// command-line program, which injects the secrets on the person's computer.
	MCPLauncher MCPMode = "launcher"
	// MCPSingleEntry writes a single entry for the product's own MCP server, which proxies
	// every server the person may use and injects the secrets.
	MCPSingleEntry MCPMode = "single-entry"
)

// Plan is the output of Adapter.Plan: what to write, and what could not be placed.
type Plan struct {
	// Ops are the operations, in the order they should be applied.
	Ops []Op
	// Skips lists the artifacts for which there is no operation, each with the reason.
	Skips []Skip
}

// Skip records an artifact that was not delivered, and why. Nothing is ever dropped silently:
// what is not in Ops is in Skips, and the person sees it in the plan.
type Skip struct {
	Resource resource.Ref
	// Reason is one of a fixed set, so that the core can decide what to tell the person and
	// what to do with the rest of the tool's plan.
	Reason SkipReason
	// Detail adds what the reason cannot say: which file, what the parser reported. It is
	// shown as it is, so it must not contain file content or anything secret.
	Detail string
}

// SkipReason says why an artifact was not delivered. The values are the names used in
// machine-readable output and do not change.
type SkipReason string

const (
	// ConfigUnreadable: a configuration file the tool already has could not be read or parsed.
	// Reported by adapters. Not being able to read means not knowing what is in the file, so
	// the core writes nothing at all to this tool in this run.
	ConfigUnreadable SkipReason = "config_unreadable"
	// ConfigUnwritable: the file was read, but the entry cannot be added without rewriting
	// something the person wrote, or it is unclear which of several files the tool uses.
	// Reported by adapters. Other artifacts of the same tool are still delivered.
	ConfigUnwritable SkipReason = "config_unwritable"
	// Unsupported: the tool has no place for this artifact and no fallback form applied.
	Unsupported SkipReason = "unsupported"
	// NeedsSecretSupport: the artifact refers to a personal secret and, in the current MCPMode,
	// there is no way to deliver it without writing the secret's value into a file.
	NeedsSecretSupport SkipReason = "needs_secret_support"
	// NameTaken: something with the target's name already exists and the manifest does not
	// list it, so it belongs to the person. Reported by the core, which knows the manifest.
	NameTaken SkipReason = "name_taken"
	// PathTooLong: the target would exceed what Windows tools reliably open. Reported by the
	// core.
	PathTooLong SkipReason = "path_too_long"
)
