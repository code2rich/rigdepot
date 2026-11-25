// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package module is the extension point for optional modules and the host they run in. Version:
// v0. The interfaces are a skeleton: they are settled when the first real module lands (M2).
//
// An optional module is a capability that is off by default, such as usage insights or AI
// review. The core never imports a module: everything a module needs comes from the Host it is
// started with, so a module can live in a repository of its own and import nothing but this
// package and the standard library.
//
// # Three switches
//
// A module is compiled in when modules/all registers it. Each module has a file of its own
// there, so that a build tag leaves it out entirely:
//
//	// modules/all/maybe_insights.go
//	//go:build !rgd_omit_insights && !rgd_core_only
//
//	package all
//
//	func init() { module.Register(insights.New()) }
//
// The tag rgd_core_only leaves out every module; the core builds and passes its tests without
// any. A module that is compiled in stays off until an administrator turns it on in the
// settings, and a deployment can lock it off for good (RGD_MODULES_DISABLED). Only then is Start
// called: a module that is off loads nothing, runs nothing and contacts nobody.
//
// # What the host gives
//
// Start receives a Host. Through it the module mounts its routes under /api/v1/x/<id>/ (behind
// the same authentication as the rest of the API), runs its own migrations in a namespace named
// after it, uses the database, schedules jobs, subscribes to domain events, writes audit entries,
// asks whether someone may do something, keeps its own settings and logs. When the module is
// turned off, the host takes back its routes, jobs and subscriptions; its tables and data stay.
//
// # Stability
//
// Everything under ext/ is public API. Before 1.0 it may change in breaking ways, and every such
// change is listed in the release notes. From 1.0 on, a breaking change means a new version of
// the package, and the old one stays for at least one major release.
package module
