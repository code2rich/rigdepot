// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package skill is the skill resource kind: skills/<code>/, a directory with a SKILL.md as the
// Agent Skills convention describes it.
//
// The front matter of SKILL.md needs a name, equal to the directory name, and a description;
// schema.json has the shape. A work tree checked out with CRLF line endings, or a SKILL.md
// saved with a byte order mark, is still a skill: only the parsing ignores them.
//
// A skill is delivered as itself, to tools that take skill directories, and nowhere else. The
// directory a tool gets holds every file of the resource byte for byte, executable bits
// included, except meta.yaml: that is what the product needs to know about the skill, not part
// of it, and the AI has no use for it.
package skill
