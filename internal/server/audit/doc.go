// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package audit keeps the append-only, hash-chained audit log and its query and export.
//
// A domain service writes its entry with Log.Append inside the transaction of the change it
// records, so that the change and its entry commit together or not at all: when the entry cannot
// be written, the change fails. An operation that was refused is recorded with Log.Record in a
// small transaction of its own.
//
// Rows are numbered without gaps and chained: each row's hash is an HMAC-SHA256, under a key
// kept sealed with the master key, of the previous row's hash and the row's own content. Someone
// who can change the database cannot recompute the chain without that key, so Log.Verify notices
// a changed, removed or inserted row. The database refuses UPDATE and DELETE on the table as
// well (migration 0002), so that a bug cannot change history either. See
// docs/architecture/06-server.md#审计.
package audit
