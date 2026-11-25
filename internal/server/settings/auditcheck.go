// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package settings

import (
	"context"
	"time"

	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// keyAuditCheck is where the daily check of the audit chain keeps how far it got.
const keyAuditCheck = "audit.verified"

// AuditCheck is the outcome of the last check of the audit chain, kept so that the next one
// checks only what was added since, and so that every instance can report it.
type AuditCheck struct {
	// LastSeq and LastHash are the last row found intact: the next check starts after it and
	// checks that the following row names this hash. 0 and "" before the first check.
	LastSeq  int64  `json:"last_seq"`
	LastHash string `json:"last_hash"`
	// At is when the check ran.
	At time.Time `json:"at"`
	// Broken is the first problem the check found, "" when the chain was intact; BrokenSeq is
	// the row where it was noticed.
	Broken    string `json:"broken,omitempty"`
	BrokenSeq int64  `json:"broken_seq,omitempty"`
}

// AuditCheck returns the outcome of the last check of the audit chain; false before the first.
// It is the server's own state: there is no permission check.
func (s *Service) AuditCheck(ctx context.Context) (AuditCheck, bool, error) {
	var c AuditCheck
	found, err := getJSON(ctx, s.db, keyAuditCheck, &c)
	return c, found, err
}

// SaveAuditCheck keeps the outcome of a check of the audit chain. It is written by the server
// itself, not on anyone's behalf, and is not audited.
func (s *Service) SaveAuditCheck(ctx context.Context, c AuditCheck) error {
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		return putJSON(ctx, tx, keyAuditCheck, c, audit.Actor{}, s.now())
	})
}
