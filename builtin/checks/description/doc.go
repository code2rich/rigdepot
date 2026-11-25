// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package description is the check rule for the quality of a skill's description. Advisory.
//
// The description in the front matter of SKILL.md is what an AI tool reads to decide whether to
// use the skill. One that is very short, or that says what the skill does but not when to use
// it, gets the skill picked at the wrong time or not at all. Length is counted in characters, so
// a Chinese and an English description are held to the same number.
//
// A SKILL.md without a description, or whose front matter cannot be read, is the structure rule's
// finding; this rule then has nothing to add.
//
// Message keys, all under check.description.:
//
//	too_short  fewer characters than the minimum    {length, min}
//	no_when    does not say when to use the skill
package description
