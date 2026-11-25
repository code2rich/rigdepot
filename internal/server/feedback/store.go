// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package feedback

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// The SQL of the feedback tables. Only this package reads and writes them.

const feedbackCols = `f.id, f.number, f.kind, f.origin, f.status, f.status_reason, f.severity, f.title, f.body,
	f.author_id, COALESCE(f.assignee_id, ''), COALESCE(f.resource_id, ''), COALESCE(f.resource_version, 0), f.context,
	COALESCE(f.score, 0), f.score_note, f.created_at, f.updated_at`

// row is feedback as stored, with what decisions about it need.
type row struct {
	Feedback
	product string        // the product of its resource, for events
	target  access.Target // what a permission decision about it looks at
}

func scanFeedback(sc interface{ Scan(...any) error }) (row, error) {
	var (
		r                              row
		kind, origin, status, severity string
		ctxJSON                        string
		created, updated               int64
	)
	err := sc.Scan(&r.ID, &r.Number, &kind, &origin, &status, &r.StatusReason, &severity, &r.Title, &r.Body,
		&r.SubmitterID, &r.AssigneeID, &r.ResourceID, &r.ResourceVersion, &ctxJSON, &r.Score, &r.ScoreNote, &created, &updated)
	if err != nil {
		return r, err
	}
	r.Type, r.Origin, r.Status, r.Severity = Type(kind), Origin(origin), Status(status), Severity(severity)
	r.CreatedAt, r.UpdatedAt = dbx.Time(created), dbx.Time(updated)
	if err := json.Unmarshal([]byte(ctxJSON), &r.Context); err != nil {
		return r, fmt.Errorf("feedback: context of FB-%d: %w", r.Number, err)
	}
	return r, nil
}

// load reads feedback matching where (a condition on f), in the given order, at most limit
// (0: all), and resolves their resources.
func (s *Service) load(ctx context.Context, q dbx.Querier, where, order string, limit int, args ...any) ([]row, error) {
	query := `SELECT ` + feedbackCols + ` FROM feedback f WHERE ` + where + ` ORDER BY ` + order
	if limit > 0 {
		query += ` LIMIT ` + strconv.Itoa(limit)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out []row
	for rows.Next() {
		r, err := scanFeedback(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.resolve(ctx, out)
}

// resolve fills in each row's resource and the target of decisions about it.
func (s *Service) resolve(ctx context.Context, rs []row) error {
	var ids []string
	seen := map[string]bool{}
	for _, r := range rs {
		if r.ResourceID != "" && !seen[r.ResourceID] {
			seen[r.ResourceID] = true
			ids = append(ids, r.ResourceID)
		}
	}
	briefs, err := s.res.Briefs(ctx, ids)
	if err != nil {
		return err
	}
	for i := range rs {
		rs[i].target = access.TargetPlatform()
		if b, ok := briefs[rs[i].ResourceID]; ok {
			rs[i].Resource = &ResourceRef{Code: b.Code, Kind: b.Kind, Name: b.Name}
			rs[i].product, rs[i].target = b.Product, b.Target
		}
	}
	return nil
}

// one reads one piece of feedback by number; ErrNotFound when there is none.
func (s *Service) one(ctx context.Context, q dbx.Querier, number int) (row, error) {
	rs, err := s.load(ctx, q, `f.number = ?`, `f.number`, 0, number)
	if err != nil {
		return row{}, err
	}
	if len(rs) == 0 {
		return row{}, ErrNotFound
	}
	return rs[0], nil
}

// timeline reads the events of feedback, oldest first.
func (s *Service) timeline(ctx context.Context, q dbx.Querier, id string) ([]Event, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, kind, COALESCE(actor_id, ''), body, data, at FROM feedback_events
		WHERE feedback_id = ? ORDER BY at, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var (
			e    Event
			kind string
			data string
			at   int64
		)
		if err := rows.Scan(&e.ID, &kind, &e.ActorID, &e.Body, &data, &at); err != nil {
			return nil, err
		}
		var d eventData
		if err := json.Unmarshal([]byte(data), &d); err != nil {
			return nil, fmt.Errorf("feedback: event %s: %w", e.ID, err)
		}
		e.Kind, e.At = EventKind(kind), dbx.Time(at)
		e.Status, e.AssigneeID, e.Score = d.Status, d.Assignee, d.Score
		out = append(out, e)
	}
	return out, rows.Err()
}

// eventData is the data column of feedback_events.
type eventData struct {
	Status   Status `json:"status,omitempty"`
	Assignee string `json:"assignee,omitempty"`
	Score    int    `json:"score,omitempty"`
}

// addEvent appends an event to the timeline of feedback id.
func addEvent(ctx context.Context, tx *dbx.Tx, id string, e Event) error {
	data, err := json.Marshal(eventData{Status: e.Status, Assignee: e.AssigneeID, Score: e.Score})
	if err != nil {
		return err
	}
	var actor any
	if e.ActorID != "" {
		actor = e.ActorID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO feedback_events (id, feedback_id, at, actor_id, kind, body, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		dbx.NewID(), id, dbx.Millis(e.At), actor, string(e.Kind), e.Body, string(data))
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int) sql.NullInt64 { return sql.NullInt64{Int64: int64(n), Valid: n != 0} }
