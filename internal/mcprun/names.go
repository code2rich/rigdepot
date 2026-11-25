// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package mcprun

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Separator joins a resource code and a tool name in the single entry: jira__search_issues.
// Resource codes have no underscore, so the first separator always ends the code.
const Separator = "__"

// MaxToolName is the longest tool name the single entry offers. The AI tools put their own
// prefix in front, and the longest one, Claude Code's mcp__<entry>__, makes 15 characters with
// the entry "rigdepot": 49 + 15 is 64, the limit of the model APIs that have the lowest one.
// Checked on Claude Code 2.1.330 (drops a tool whose full name is over 128), Codex 0.172.0
// (cuts the name and adds a hash past 128 for namespace and name) and OpenCode 2.4.1 (no limit
// of its own; the provider's applies).
const MaxToolName = 49

// hashLen is how many hex digits of the digest end a shortened name.
const hashLen = 8

// ToolName is the name the single entry offers a tool of a server under: the resource code,
// Separator and the tool's name, with every character other than letters, digits, "_" and "-"
// replaced by "_". A name longer than MaxToolName is cut and ends in "_" and the first hashLen
// hex digits of the SHA-256 of the full name, so that it stays unique and the same every time.
func ToolName(code, tool string) string {
	full := code + Separator + sanitize(tool)
	if len(full) <= MaxToolName {
		return full
	}
	sum := sha256.Sum256([]byte(code + Separator + tool))
	return full[:MaxToolName-hashLen-1] + "_" + hex.EncodeToString(sum[:])[:hashLen]
}

// sanitize replaces what tool names may not have.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, s)
}
