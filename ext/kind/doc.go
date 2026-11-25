// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package kind is the resource kind extension point (E2): parsing, schema, rendering into
// artifacts and fallback forms. Version: v0.
//
// A resource kind knows one shape of resource in the repository: what its files look like, how
// to validate them, and which artifacts it can become. It knows nothing about any tool; where an
// artifact goes is the business of an adapter (package adapter).
//
// # Forms and falling back
//
// A kind lists the forms it can be delivered in, best first. For each resource and tool the core
// takes the first form whose needs the tool's capabilities meet. A workflow, for example, is a
// slash command where the tool has those and a skill elsewhere:
//
//	func (workflow) Forms() []kind.Form {
//		return []kind.Form{
//			{Needs: kind.Needs{Commands: true}, Render: asCommand},
//			{Needs: kind.Needs{Skills: true}, Degraded: "workflow.as_skill", Render: asSkill},
//		}
//	}
//
// The fallback rules are data that lives with the kind, not conditions scattered over the
// adapters. When no form fits, the resource is not delivered to that tool and the plan says so.
//
// # Adding a kind
//
// Ask first whether the new kind can be rendered as one of the existing artifacts. If it can, it
// is one directory under builtin/kinds with New exported, its JSON Schema next to it, and one
// line in builtin/all; no adapter changes. A kind that needs a new artifact shape affects every
// adapter and starts as a discussion, not as a pull request.
//
// # Stability
//
// Everything under ext/ is public API. Before 1.0 it may change in breaking ways, and every such
// change is listed in the release notes. From 1.0 on, a breaking change means a new version of
// the package, and the old one stays for at least one major release.
package kind
