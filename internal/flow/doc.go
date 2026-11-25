// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package flow keeps the workflows synced to this computer and hands out their steps one at a
// time: rgd flow next.
//
// A workflow reaches an AI tool as a short text (a slash command, or a skill where the tool has
// no slash commands) that tells the AI to ask the command line for the next step. The steps
// themselves stay here:
//
//	flows/<code>.json                       the workflow as last synced (Keep writes it)
//	flows/runs/<code>-<directory hash>.json one run in progress, per project directory
//
// both in the local data directory. A run belongs to a workflow and the directory it runs in,
// because the checks that are commands run there, and the same workflow can be under way in two
// projects at once.
//
// # Handing out a step
//
// Next starts a run with the first step when there is none. Otherwise it checks the step handed
// out last, and hands out the following one only when every check has passed:
//
//   - a check by the AI passes when the AI states its evidence (Input.Evidence);
//   - a check by the person passes when the AI says the person confirmed it (Input.Confirmed);
//   - a check by command passes when the command, run by the system's shell in the run's
//     directory, exits with 0. Commands are only run once the other checks have passed, so
//     that a forgotten --evidence does not cost a test run.
//
// A step whose "then" is "confirm" waits, once its checks have passed, until the person says to go
// on (Input.Confirmed). After the last step the run is over and its file is removed.
//
// A workflow that a sync changed while a run was under way is not continued from a step that may
// no longer mean the same: Next refuses with ErrChanged until the run is restarted. A file that
// cannot be read is unknown, not "no run": Next refuses rather than starting over.
package flow
