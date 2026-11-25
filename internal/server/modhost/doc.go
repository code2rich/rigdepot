// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package modhost is the module host: it starts the modules enabled in settings and hands them
// routes, migrations, jobs and events.
//
// Which modules are on is the setting "modules.enabled", a JSON array of module IDs. On start the
// host reads it and starts those modules that are compiled in and not locked by the deployment
// (RGD_MODULES_DISABLED). A setting that cannot be read means "unknown", and the host starts
// none: a module that collects data must never come on by accident.
//
// Turning a module on starts it and then records it in the setting; turning it off records that
// and then stops it: its routes answer 404 at once, its jobs are removed (a run in progress is
// canceled), its subscriptions end, and Stop is called when it has one. Its tables and data
// stay, and so do its migrations.
//
// Each module gets its own view of the core (module.Host): routes under /api/v1/x/<id>/, the
// migration namespace <id>, jobs named <id>.<name>, settings under modules.<id>., and audit
// actions that must start with <id>. so that a module cannot write entries that look like the
// core's. Audit, permissions and settings come from the domain packages through the small
// interfaces in Deps; when one is missing, what depends on it fails closed.
package modhost
