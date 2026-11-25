// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package audit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// hash is HMAC-SHA256(key, prev_hash ‖ the row's content), in hex. The
// content is every column but the hash itself, each written with its length
// first so that no two different rows encode the same. A row without an
// actor (NULL) counts as an empty actor.
func (l *Log) hash(r Row) string {
	m := hmac.New(sha256.New, l.key)
	var n [8]byte
	str := func(s string) {
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		m.Write(n[:])
		m.Write([]byte(s))
	}
	num := func(v int64) {
		binary.BigEndian.PutUint64(n[:], uint64(v))
		m.Write(n[:])
	}
	str("audit row v1")
	str(r.PrevHash)
	num(r.Seq)
	num(dbx.Millis(r.At))
	str(string(r.Category))
	str(r.Actor.UserID)
	str(string(r.Actor.Origin))
	str(r.Actor.ViaTool)
	str(r.Actor.DeviceID)
	str(r.Method)
	str(r.Action)
	str(r.Object.Kind)
	str(r.Object.ID)
	str(r.Object.Label)
	str(r.Change)
	str(string(r.Result))
	str(r.Reason)
	return hex.EncodeToString(m.Sum(nil))
}

// Report is what Verify found.
type Report struct {
	Checked  int64  // rows checked
	LastSeq  int64  // the last row checked, 0 when none
	LastHash string // its hash: a later Verify from LastSeq+1 can start here
	// Broken is the first problem found, nil when the chain is intact.
	Broken *Break
}

// Break is where the chain is broken.
type Break struct {
	Seq     int64  // the row where it was noticed
	Problem string // what is wrong, for people
}

func (b *Break) Error() string { return fmt.Sprintf("audit log broken at #%d: %s", b.Seq, b.Problem) }

// Verify checks the chain from row from (1 for all of it) to the end: that
// the rows are numbered without gaps, that each one names its predecessor's
// hash, and that each hash is right for the row's content under the chain
// key. It stops at the first problem. A changed row, a removed row and an
// inserted row each break it.
//
// From a later row it trusts the hash that row names as its predecessor;
// checking incrementally, a caller passes the LastHash of the previous run as
// anchor to check that link too ("" skips it).
func (l *Log) Verify(ctx context.Context, from int64, anchor string) (Report, error) {
	if from < 1 {
		from = 1
	}
	var rep Report
	rows, err := l.db.QueryContext(ctx, `SELECT `+rowColumns+` FROM audit_log WHERE seq >= ? ORDER BY seq`, from)
	if err != nil {
		return rep, fmt.Errorf("audit: verify: %w", err)
	}
	defer rows.Close()
	want := from
	prev := anchor
	if from == 1 {
		prev = genesis
	}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return rep, fmt.Errorf("audit: verify: %w", err)
		}
		var problem string
		switch {
		case r.Seq != want:
			problem = fmt.Sprintf("expected row #%d next; rows are missing or were inserted", want)
		case prev != "" && r.PrevHash != prev:
			problem = "it does not follow the previous row: a row before it was changed, removed or inserted"
		case !hmac.Equal([]byte(r.Hash), []byte(l.hash(r))):
			problem = "its content does not match its hash: the row was changed"
		}
		if problem != "" {
			rep.Broken = &Break{Seq: r.Seq, Problem: problem}
			return rep, nil
		}
		rep.Checked++
		rep.LastSeq, rep.LastHash = r.Seq, r.Hash
		prev, want = r.Hash, r.Seq+1
	}
	if err := rows.Err(); err != nil {
		return rep, fmt.Errorf("audit: verify: %w", err)
	}
	if rep.Checked == 0 && from > 1 {
		// Asked to start after the end: the rows up to from-1 must still be
		// there, or the end of the log was cut off.
		var n int64
		err := l.db.QueryRowContext(ctx, `SELECT seq FROM audit_log WHERE seq = ?`, from-1).Scan(&n)
		if errors.Is(err, sql.ErrNoRows) {
			rep.Broken = &Break{Seq: from - 1, Problem: "the row is gone: the end of the log was removed"}
		} else if err != nil {
			return rep, fmt.Errorf("audit: verify: %w", err)
		}
	}
	return rep, nil
}

const rowColumns = `seq, at, category, actor_id, origin, via_tool, device_id, method, action,
	object_kind, object_id, object_label, change, result, reason, prev_hash, hash`

func scanRow(rows interface{ Scan(...any) error }) (Row, error) {
	var r Row
	var at int64
	var actor sql.NullString
	err := rows.Scan(&r.Seq, &at, &r.Category, &actor, &r.Actor.Origin, &r.Actor.ViaTool, &r.Actor.DeviceID,
		&r.Method, &r.Action, &r.Object.Kind, &r.Object.ID, &r.Object.Label, &r.Change, &r.Result, &r.Reason,
		&r.PrevHash, &r.Hash)
	r.At = dbx.Time(at)
	r.Actor.UserID = actor.String
	return r, err
}
