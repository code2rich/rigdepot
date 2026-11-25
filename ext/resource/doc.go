// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package resource defines the value types shared by every extension point: resource identifiers,
// kinds, scopes, policies, stages, metadata and bundles. Version: v0.
//
// A resource is one unit a team shares with the AI coding tools on its members' computers: a
// skill, an MCP server, a workflow and so on. Resources live in a resource repository, an
// ordinary Git repository in which the top-level directory decides the kind and the directory or
// file name is the resource's code:
//
//	skills/unit-testing/     a skill, code "unit-testing"
//	mcp/jira.yaml            an MCP server, code "jira"
//
// The types here carry no behaviour beyond validating their own shape. Reading a repository,
// computing digests and packing bundles is the core's job; adapters, resource kinds and check
// rules only receive these values.
//
// # Stability
//
// Everything under ext/ is public API. Before 1.0 it may change in breaking ways, and every such
// change is listed in the release notes. From 1.0 on, a breaking change means a new version of
// the package, and the old one stays for at least one major release.
package resource
