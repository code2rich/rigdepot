// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package delivery computes desired state, distributes resource bundles and records install
// results.
//
// # Desired state
//
// What a person's computers should have (Desired) is computed fresh on every request from
// three things: what the person sees (catalog.Service.Catalog, which asks access for their
// scopes: the organisation, the products they are in, their own personal resources), each
// resource's policy, and the person's own choices (user_resource_prefs):
//
//   - required: always; the person cannot turn it off (SetPreference refuses).
//   - recommended: unless the person turned it off.
//   - optional: only when the person turned it on.
//
// Then dependencies come along: a resource that is delivered brings the resources it uses (a
// workflow's steps, an agent's bindings: catalog.Resource.References), and those bring theirs,
// whatever their own policy or the person's choice says; they are marked ReasonDependency with
// the resources that need them. A dependency the person cannot see is not delivered: the scope
// is a permission boundary, and a workflow in the organisation's scope cannot hand out a
// product's resources to people outside it.
//
// # Bundles
//
// A bundle is handed out (Bundle) only to someone who sees a published resource that has a
// version in it; otherwise it does not exist for them (ErrNotFound).
//
// # Install reports
//
// After a sync the command line reports what is on the computer (Report): per resource and
// tool, the version, whether it is installed, skipped, failed or removed, and a stable reason
// code. A report never carries a path or content: the reason codes are checked against a narrow
// pattern, and nothing else in it is free text. The last report of each device is kept in
// device_syncs (the command-line version, the result, the tools it found); its installs in
// installs. They answer "where is it installed" for the catalog pages, the impact of disabling a
// resource, the devices list, and the counts of the tool adapters page and the home page
// (Summary).
//
// Counts cover the devices that are signed in (Options.LiveDevices): a computer that was signed
// out no longer counts, though its last report stays.
package delivery
