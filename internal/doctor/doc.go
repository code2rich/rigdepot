// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package doctor holds the self-check items of the command-line program, one file per item.
//
// A check looks at one thing on this computer (the local data directory, the manifest, git, the
// AI tools …) and says what it found as one or more Results. Checks only look: they never
// repair, and they report what they cannot read as Unknown, never as fine. The command "doctor"
// in package cli runs every check in Checks and renders the results for people or as JSON.
//
// # Adding a check
//
//  1. Write <item>.go in this package: a type with ID, Title and Run. ID is stable lower-case
//     English with hyphens; it is the "check" field of the JSON output and cannot change after a
//     release. Title is the message key of its name (msg.Doctor<Item>Title). Run returns at least
//     one Result. Every Result has a Status, a stable Code (lower-case
//     English with underscores, what scripts match on) and a Message; a Hint says what to do
//     when the status is not OK.
//  2. Add one line to the list in Checks, where it belongs in the order people read.
//  3. Write the messages under doctor.<item>.* in locales/en/cli.json and
//     locales/zh-CN/cli.json, doctor.<item>.title among them, then run "go run ./tools/dev gen"
//     for the msg constants.
//  4. Write <item>_test.go with at least one case that passes and one that does not, each
//     against a temporary home directory set up by the test (see newTestEnv).
//
// Everything a check needs from the computer comes from Env, so that tests can describe any
// computer. A check that needs something Env does not have yet adds a field to Env and the
// command fills it in.
package doctor
