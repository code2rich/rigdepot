// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package configedit makes surgical, format-preserving edits to other tools' JSON, JSONC and TOML
// configuration and validates the result.
//
// Every function here is pure: it takes the bytes of a file and returns new bytes. Nothing in
// this package touches the file system; reading, locking, backing up and replacing the file is
// the executor's job (internal/executor).
//
// # What an edit looks like
//
// Set never re-serialises a file. It parses the file only to find where things are, then
// replaces one contiguous run of bytes: everything before and after that run is returned
// exactly as it was, including comments, key order, trailing commas, CRLF line endings, a byte
// order mark and a missing newline at the end of the file. The Edit it returns records that run
// (where it is, what was there, what was put there), so that Revert can put the original bytes
// back.
//
// Before Set returns, it parses its own result and checks that the only semantic difference from
// the input is the one key it was asked to set, together with any parent objects it had to
// create. A result that fails this check is never returned (ErrSelfCheck): that would be a defect
// here, not something about the person's file.
//
// # JSON and JSONC
//
// The parser is github.com/tailscale/hujson, which keeps comments and trailing commas. A new
// member is appended at the end of its object, on its own line and with the indentation of its
// siblings when the object spans several lines, inline otherwise. Parents that do not exist yet
// are created. Strict JSON (Format JSON) must parse as standard JSON before and after.
//
// # TOML
//
// The parser is github.com/pelletier/go-toml/v2. The value of a TOML key must be a table (a
// map), and Set appends it at the end of the file as "[a.b]" followed by its keys, with nested
// tables as their own "[a.b.c]" headers. A table that was written with a header can be replaced
// or deleted as a block; a table written in any other way, in particular an inline table such as
// mcp_servers = { … }, cannot be extended without rewriting the person's own text, and Set
// reports ErrInlineTable or ErrUnwritable instead.
//
// # Undoing
//
// Revert first tries to put the recorded bytes back. That succeeds byte for byte when nobody
// rewrote that part of the file since; the result is still checked semantically before it is
// returned. When the bytes are no longer there (a tool reformatted the file), Revert falls back to
// removing the key semantically, or to restoring its previous value for an edit that replaced
// one, and says which of the two it did.
package configedit
