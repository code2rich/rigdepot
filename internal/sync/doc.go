// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package sync is the sync engine: detect, render, plan, consent, apply, report; plus uninstall
// and drift detection.
//
// # A sync
//
//	desired.State ─┐
//	               ├─ Engine.Plan      detect every tool, render every resource for it (the kind's
//	manifest ──────┘                   forms, in order: the first the tool can take; a later one
//	                                   is a fallback), let the adapter plan, drop what a tool
//	                                   already reads from another tool's directory (alsoReads),
//	                                   compare each target with the manifest and the disk
//	                  Engine.Preview   the same operations, executed in a sandbox copy
//	                  Plan.Approve     who agreed to which change of a tool's global configuration
//	                  Engine.Lock      the cross-process lock; settles what a crash left behind
//	                  Session.Apply    the same operations again, on the real home directory
//
// Uninstall is a plan too (Engine.PlanUninstall): every manifest entry undone, newest first. The
// state of what was written is Engine.Status.
//
// # The comparison
//
// For every target (tool, operation, path, key) the engine compares three digests: the one the
// manifest recorded, the one on disk now, and the one the new plan wants
// (docs/architecture/05-adapters.md, "计划、应用、撤销、漂移"):
//
//	manifest  disk                     plan      action
//	no        absent                   yes       ActionCreate     +
//	no        present                  yes       ActionTaken      !  the person's own, left alone
//	yes       as recorded              same      ActionNone
//	yes       as recorded              other     ActionUpdate     ~
//	yes       changed                  yes       ActionModified   !  ActionOverwrite with --overwrite
//	yes       changed, to the wanted   yes       ActionRecord        only the manifest changes
//	yes       absent                   yes       ActionDeleted    !  ActionRestore with --overwrite
//	yes       any                      no        ActionRevert     -
//	          unreadable                         the tool is left out of this run entirely
//
// A key of a tool's configuration that gets a new value (ActionUpdate, ActionOverwrite) is undone
// and set again rather than replaced in place, so that its entry always records the exact bytes
// it added and uninstall restores the file byte for byte.
//
// Not being able to read means not knowing, never "absent" or "equal": a tool whose detection,
// configuration or any target cannot be read is left out of the run, and nothing of it is written
// or undone. The same holds for resources: what was written for a resource the repository could
// not read, that could not be rendered, or that the adapter skipped stays as it is. Only what is
// no longer wanted at all is undone.
//
// # Consent
//
// Changing a tool's global configuration (SetKey, undoing a key, Exec) needs the person's consent
// the first time per file; it is remembered in state/consent.json. Plan.Approve encodes the rule:
// --skip-global agrees to nothing, a file agreed to before needs no question, --yes-global agrees
// to everything in this plan, a person at a terminal is asked; without a terminal and without
// --yes-global, nothing is agreed to and the global changes wait (exit code 3).
//
// # Preview and write are the same operations
//
// Engine.Preview hands exactly the operations of Plan.Items to sandbox.Preview and carries them
// out with the executor rooted in the sandbox; Session.Apply carries out the same items with the
// executor rooted in the real home directory. Nothing is planned twice.
package sync
