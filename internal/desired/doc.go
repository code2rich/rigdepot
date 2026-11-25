// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package desired computes the desired state from either the server API or a local repository, in
// one common shape.
//
// The desired state is what should be on this computer after a sync: which resources, in which
// version, parsed by their kind and ready to be rendered for each tool. The sync engine (package
// sync) compares it with the manifest and the disk; it does not care where it came from.
//
// # From a repository (rgd sync --repo)
//
//	ReadRepo:  git (package gitrepo) → git archive → bundle.ReadTree → repoformat.Read
//	FromRepo:  every resource the repository holds, filtered by Filter
//
// Filtering uses each resource's effective metadata (repoformat.Config.Effective: the resource's
// own values, else the repository's defaults):
//
//   - Scope: "org" and an unset scope are for everyone. A product scope is for the products the
//     person chose (--product, or repo.products in the configuration). "me" is a personal
//     resource; without a server nobody can tell whose, so it is not delivered.
//   - Audience: when Filter.Audiences lists some, only resources for those audiences (or with no
//     audience) are delivered.
//   - Policy and the person's own selection (Selection, state/selection.json): required is always
//     delivered; recommended, and an unset policy, unless the person disabled it; optional only
//     when the person enabled it.
//
// A resource filtered out is Excluded: it is not wanted, and whatever an earlier sync wrote for it
// is undone. A resource the repository holds but that could not be read is Failed: its state is
// unknown, not absent, and what was written for it is left as it is.
//
// # From the server (rgd sync when signed in)
//
//	FromServer: GET /sync/desired → each resource's bundle (cache/bundles/, else GET
//	            /sync/bundles/{digest}, checked against its address) → bundle.Unpack →
//	            the kind's Parse
//
// The server does the filtering (scopes, policies, the person's choices, dependencies) and
// sends only what is delivered, with the metadata in force; the result is the same State. What
// it says is kept in state/desired.json. When the server cannot be reached, that kept state is
// used instead (ServerState.Cached) and the caller removes nothing: what the server would say
// now is unknown. A resource whose bundle cannot be had, unpacked or parsed is Failed.
//
// The server also says which tools nobody writes to and which command-line versions it accepts
// (Org): a program older than the minimum gets ErrTooOld, even from a kept state.
package desired
