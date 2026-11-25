// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package agent is the agent resource kind: agents/<code>/, a role with fixed duties. schema.json
// has the shape of its agent.yaml.
//
//	agents/<code>/
//	├── agent.yaml          id (the code), entry, own skills, binds, mcp, local
//	├── AGENT.md            the role's instructions (the entry)
//	├── meta.yaml           the metadata every resource has
//	├── .env.example        optional: local configuration, as for a skill
//	├── skills/<name>/      the agent's own skills, each with a SKILL.md, each listed in agent.yaml
//	└── memory/ …           optional seeds of the local directories
//
// # Local directories
//
// The directories listed under local (memory/, workspace/) belong to each person's computer once
// the agent is there: what the agent writes into them is the person's. They do not count towards
// the content digest (kind.LocalKeeper), and the skill directory the agent is delivered in
// carries them in adapter.SkillDir.Local, so that the executor keeps what is in them when the
// agent is updated and copies it to kept/ before the agent is removed. What the repository has
// in them is a seed, written when the agent first arrives.
//
// # Forms
//
// In order:
//
//  1. A sub-agent (adapter.SubAgent) named the prefix followed by the code, its body the entry's
//     text, in a tool with sub-agents and skills (Claude Code, OpenCode; OpenCode's adapter adds
//     "mode: subagent"). When the agent has own skills, local directories or other files, a skill
//     directory of the same name holds them and the sub-agent's body points at it.
//  2. The sub-agent alone, for a tool with sub-agents and no skills, when the agent needs no
//     directory.
//  3. A skill of the same name that is the whole agent, marked with the explanation
//     "agent.as_skill", in a tool with skills and no sub-agents (Codex).
//
// What the agent binds and the MCP servers it depends on are named in its instructions, and are
// references the core checks (kind.Referrer). The environment resources the product design lets
// an agent bind (databases, servers) are not in a repository and are not read yet.
package agent
