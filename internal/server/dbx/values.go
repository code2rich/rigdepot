// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package dbx

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
)

// The portable subset stores values like this (06-server.md#怎么用同一套迁移):
//
//	primary keys  TEXT, a UUIDv7 made by NewID; no auto-increment columns
//	times         BIGINT, UTC milliseconds (Millis, Time)
//	booleans      BOOLEAN, TRUE and FALSE; Go bool values work on both sides
//	structures    JSON in TEXT; no JSON operators in SQL
//	upsert        INSERT … ON CONFLICT (…) DO UPDATE SET x = excluded.x
//	counters      MAX(n) + 1 inside InTx after Tx.Lock on a key of your own
//
// A number shown to people (FB-42, v5) is a counter, never a primary key.

// Millis converts a time to what a time column stores.
func Millis(t time.Time) int64 { return t.UnixMilli() }

// Time converts a stored time back, in UTC.
func Time(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

var (
	idMu   sync.Mutex
	lastMs int64
	lastID [16]byte
)

// NewID returns a new UUIDv7 (RFC 9562) as 36 characters. IDs made later
// sort after earlier ones, also within one millisecond in one process, so
// that rows inserted in order can be listed by key.
func NewID() string {
	idMu.Lock()
	defer idMu.Unlock()
	ms := time.Now().UnixMilli()
	var u [16]byte
	if ms <= lastMs {
		// Same millisecond (or the clock went back): count up from the
		// last ID instead of drawing new randomness, to keep the order.
		u = lastID
		for i := 15; i >= 6; i-- {
			u[i]++
			if u[i] != 0 {
				break
			}
		}
		u[6] = u[6]&0x0f | 0x70
		u[8] = u[8]&0x3f | 0x80
	} else {
		if _, err := rand.Read(u[6:]); err != nil {
			panic("dbx: no randomness: " + err.Error())
		}
		u[0], u[1], u[2], u[3], u[4], u[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
		u[6] = u[6]&0x0f | 0x70 // version 7
		u[8] = u[8]&0x3f | 0x80 // variant 10
		lastMs = ms
	}
	lastID = u
	var s [36]byte
	hex.Encode(s[0:8], u[0:4])
	s[8] = '-'
	hex.Encode(s[9:13], u[4:6])
	s[13] = '-'
	hex.Encode(s[14:18], u[6:8])
	s[18] = '-'
	hex.Encode(s[19:23], u[8:10])
	s[23] = '-'
	hex.Encode(s[24:], u[10:])
	return string(s[:])
}

// IsUniqueViolation reports whether err is a violation of a primary key or
// unique constraint, on either database.
func IsUniqueViolation(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		// SQLITE_CONSTRAINT_UNIQUE and SQLITE_CONSTRAINT_PRIMARYKEY.
		return se.Code() == 2067 || se.Code() == 1555
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code == "23505"
	}
	return false
}
