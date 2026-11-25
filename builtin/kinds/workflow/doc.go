// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package workflow is the workflow resource kind: workflows/<code>.yaml, ordered steps, each done
// by a skill or an agent, with checks at its end and stops where the person decides whether to go
// on. schema.json has the shape.
//
// # The steps are handed out one at a time
//
// What reaches the AI tool is not the steps. It is a short text that tells the AI to ask the
// command line for the next step (<cli> flow next <code>), do it, and ask again; the command line
// runs the checks that are commands, takes the AI's evidence for the checks it makes and the
// person's word for theirs, and hands out the next step only when every check of the step before
// has passed. The steps themselves reach the command line through kind.Flow, and the codes they
// use through kind.Referrer, so that a workflow that uses a resource the repository does not
// have is refused.
//
// # Forms
//
// In order:
//
//  1. A slash command (adapter.Command), named the prefix followed by the code, in a tool that
//     has slash commands (Claude Code, OpenCode). Its body ends with $ARGUMENTS, which those
//     tools replace with what the person typed after the command.
//  2. A skill of the same name, marked with the explanation "workflow.as_skill", in a tool that
//     has skills but no slash commands (Codex, decision D6).
package workflow
