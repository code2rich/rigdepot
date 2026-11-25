// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package notify

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// The in-app notifications: one row per recipient in the notifications table, holding the
// template and its parameters rather than text, so that each is rendered in the language of
// whoever reads it, when they read it.

// Page sizes of List, as in the contract's Limit parameter.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
	// MaxMarkRead is how many ids MarkRead takes at once.
	MaxMarkRead = 200
)

// Errors of List and MarkRead: the request is wrong.
var (
	ErrBadCursor = errors.New("notify: the cursor is not one this server gave out")
	ErrBadLimit  = fmt.Errorf("notify: the page size must be between 1 and %d", MaxPageSize)
	ErrTooMany   = fmt.Errorf("notify: at most %d notifications at once", MaxMarkRead)
)

// Notification is one in-app notification, rendered in its reader's language.
type Notification struct {
	ID        string
	Kind      string // the event, such as access.join_requested
	Title     string
	Body      string
	Link      string // a path in the console, such as /products; may be empty
	CreatedAt time.Time
	ReadAt    *time.Time // nil while unread
}

// ListOptions narrow List.
type ListOptions struct {
	UnreadOnly bool
	Limit      int    // 0 means DefaultPageSize; at most MaxPageSize
	Cursor     string // NextCursor of the previous page
}

// Page is one page of someone's notifications, newest first.
type Page struct {
	Items      []Notification
	NextCursor string // empty on the last page
	Unread     int    // how many of all their notifications are unread
}

// insertNotification writes one in-app notification in tx.
func insertNotification(ctx context.Context, tx *dbx.Tx, at time.Time, userID, kind, template string, params map[string]string, link string) error {
	raw := []byte("{}")
	if len(params) > 0 {
		var err error
		if raw, err = json.Marshal(params); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO notifications (id, user_id, kind, message_key, params, link, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, dbx.NewID(), userID, kind, template, string(raw), link, dbx.Millis(at))
	return err
}

// List returns a page of userID's notifications, newest first, rendered in locale (empty: the
// server's default).
func (s *Service) List(ctx context.Context, userID, locale string, o ListOptions) (Page, error) {
	limit := o.Limit
	switch {
	case limit == 0:
		limit = DefaultPageSize
	case limit < 0 || limit > MaxPageSize:
		return Page{}, ErrBadLimit
	}
	query := `SELECT id, kind, message_key, params, link, created_at, read_at FROM notifications WHERE user_id = ?`
	args := []any{userID}
	if o.UnreadOnly {
		query += ` AND read_at IS NULL`
	}
	if o.Cursor != "" {
		at, id, err := parseCursor(o.Cursor)
		if err != nil {
			return Page{}, err
		}
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, at, at, id)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	page := Page{Items: []Notification{}}
	var lastAt int64
	for rows.Next() {
		var n Notification
		var template, params string
		var created int64
		var read sql.NullInt64
		if err := rows.Scan(&n.ID, &n.Kind, &template, &params, &n.Link, &created, &read); err != nil {
			return Page{}, err
		}
		if len(page.Items) == limit {
			page.NextCursor = cursor(lastAt, page.Items[limit-1].ID)
			break
		}
		var p map[string]string
		if err := json.Unmarshal([]byte(params), &p); err != nil {
			return Page{}, fmt.Errorf("notify: the parameters of notification %s: %w", n.ID, err)
		}
		n.Title, n.Body = s.render.text(locale, template, p)
		n.CreatedAt = dbx.Time(created)
		if read.Valid {
			t := dbx.Time(read.Int64)
			n.ReadAt = &t
		}
		lastAt = created
		page.Items = append(page.Items, n)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	rows.Close()
	page.Unread, err = s.Unread(ctx, userID)
	return page, err
}

// Unread counts userID's unread notifications.
func (s *Service) Unread(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

// MarkRead marks the given notifications of userID read and returns how many were unread. Ids
// that are not userID's, or not notifications at all, are ignored: nobody learns whether
// someone else's notification exists.
func (s *Service) MarkRead(ctx context.Context, userID string, ids []string) (int, error) {
	if len(ids) > MaxMarkRead {
		return 0, ErrTooMany
	}
	if len(ids) == 0 {
		return 0, nil
	}
	args := []any{dbx.Millis(s.now()), userID}
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL AND id IN (`+
		strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// MarkAllRead marks every notification of userID read and returns how many were unread.
func (s *Service) MarkAllRead(ctx context.Context, userID string) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL`,
		dbx.Millis(s.now()), userID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// cursor is the position after a notification: its time and id, opaque to the caller.
func cursor(at int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at, 10) + "." + id))
}

func parseCursor(c string) (int64, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, "", ErrBadCursor
	}
	at, id, ok := strings.Cut(string(raw), ".")
	if !ok || id == "" {
		return 0, "", ErrBadCursor
	}
	n, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return 0, "", ErrBadCursor
	}
	return n, id, nil
}
