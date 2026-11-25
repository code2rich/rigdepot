// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package audit

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Filter selects entries. Zero fields select everything.
type Filter struct {
	Category   Category
	ActorID    string
	Action     string // exact, or a prefix ending in a dot: "resource."
	ObjectKind string
	ObjectID   string
	Result     Result
	Since      time.Time // inclusive
	Until      time.Time // exclusive
	// Concerning keeps only entries made by this user or about them (an
	// object of kind "user" with their id). It is what someone who may not
	// read the whole log gets; the caller decides when it applies.
	Concerning string
}

func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, a ...any) {
		conds = append(conds, cond)
		args = append(args, a...)
	}
	if f.Category != "" {
		add("category = ?", string(f.Category))
	}
	if f.ActorID != "" {
		add("actor_id = ?", f.ActorID)
	}
	if prefix, ok := strings.CutSuffix(f.Action, "."); ok && prefix != "" {
		// SUBSTR rather than LIKE: action names contain "_", a LIKE wildcard.
		add("SUBSTR(action, 1, ?) = ?", len(f.Action), f.Action)
	} else if f.Action != "" {
		add("action = ?", f.Action)
	}
	if f.ObjectKind != "" {
		add("object_kind = ?", f.ObjectKind)
	}
	if f.ObjectID != "" {
		add("object_id = ?", f.ObjectID)
	}
	if f.Result != "" {
		add("result = ?", string(f.Result))
	}
	if !f.Since.IsZero() {
		add("at >= ?", dbx.Millis(f.Since))
	}
	if !f.Until.IsZero() {
		add("at < ?", dbx.Millis(f.Until))
	}
	if f.Concerning != "" {
		add("(actor_id = ? OR (object_kind = 'user' AND object_id = ?))", f.Concerning, f.Concerning)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// Page is one page of a search.
type Page struct {
	Rows       []Row
	NextCursor string // "" on the last page
}

// The page sizes of the contract.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// ErrBadCursor means the cursor is not one this package handed out.
var ErrBadCursor = errors.New("audit: the cursor is not valid")

type cursor struct {
	Seq int64 `json:"s"`
}

func encodeCursor(seq int64) string {
	b, _ := json.Marshal(cursor{seq})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, ErrBadCursor
	}
	var c cursor
	if err := json.Unmarshal(b, &c); err != nil || c.Seq < 1 {
		return 0, ErrBadCursor
	}
	return c.Seq, nil
}

// Query returns the entries that match f, newest first, limit at a time
// (DefaultLimit when 0, at most MaxLimit). cursor is the NextCursor of the
// previous page, or "" for the first. Paging by sequence number is stable
// while entries are added.
func (l *Log) Query(ctx context.Context, f Filter, limit int, cursor string) (Page, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	where, args := f.where()
	if cursor != "" {
		before, err := decodeCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		if where == "" {
			where = " WHERE seq < ?"
		} else {
			where += " AND seq < ?"
		}
		args = append(args, before)
	}
	args = append(args, limit+1)
	rows, err := l.db.QueryContext(ctx, `SELECT `+rowColumns+` FROM audit_log`+where+` ORDER BY seq DESC LIMIT ?`, args...)
	if err != nil {
		return Page{}, fmt.Errorf("audit: query: %w", err)
	}
	defer rows.Close()
	var page Page
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return Page{}, fmt.Errorf("audit: query: %w", err)
		}
		page.Rows = append(page.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("audit: query: %w", err)
	}
	if len(page.Rows) > limit {
		page.Rows = page.Rows[:limit]
		page.NextCursor = encodeCursor(page.Rows[limit-1].Seq)
	}
	return page, nil
}

// Rows iterates over entries, oldest first. Close it.
type Rows struct {
	rows *sql.Rows
	row  Row
	err  error
}

// Scan returns every entry that matches f, oldest first, for an export.
func (l *Log) Scan(ctx context.Context, f Filter) (*Rows, error) {
	where, args := f.where()
	rows, err := l.db.QueryContext(ctx, `SELECT `+rowColumns+` FROM audit_log`+where+` ORDER BY seq`, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: export: %w", err)
	}
	return &Rows{rows: rows}, nil
}

// Next moves to the next entry; false at the end or on an error (Err).
func (r *Rows) Next() bool {
	if r.err != nil || !r.rows.Next() {
		return false
	}
	r.row, r.err = scanRow(r.rows)
	return r.err == nil
}

// Row is the current entry.
func (r *Rows) Row() Row { return r.row }

// Err is the error that ended the iteration, if any.
func (r *Rows) Err() error {
	if r.err != nil {
		return r.err
	}
	return r.rows.Err()
}

// Close releases the rows.
func (r *Rows) Close() error { return r.rows.Close() }

// Names turns ids into what people read in an export. Either function may
// be nil; the id is written then.
type Names struct {
	User   func(id string) string // a user's account name
	Device func(id string) string // a computer's name
}

// CSVHeader is the export's header row.
var CSVHeader = []string{"seq", "at", "category", "actor", "origin", "via_tool", "device", "action",
	"object_kind", "object_id", "object_label", "change", "result", "reason", "method"}

// WriteCSV writes the rows as RFC 4180 CSV with a header row. Cells that a
// spreadsheet would run as a formula are prefixed with an apostrophe:
// labels and reasons come from people.
func WriteCSV(w io.Writer, rows *Rows, names Names) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVHeader); err != nil {
		return err
	}
	name := func(f func(string) string, id string) string {
		if id == "" || f == nil {
			return id
		}
		if n := f(id); n != "" {
			return n
		}
		return id
	}
	for rows.Next() {
		r := rows.Row()
		record := []string{
			strconv.FormatInt(r.Seq, 10), r.At.Format(time.RFC3339), string(r.Category),
			name(names.User, r.Actor.UserID), string(r.Actor.Origin), r.Actor.ViaTool, name(names.Device, r.Actor.DeviceID),
			r.Action, r.Object.Kind, r.Object.ID, r.Object.Label, r.Change, string(r.Result), r.Reason, r.Method,
		}
		for i := 3; i < len(record); i++ {
			record[i] = defuse(record[i])
		}
		if err := cw.Write(record); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

// defuse keeps a spreadsheet from treating a cell as a formula.
func defuse(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
