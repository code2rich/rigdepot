// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package feedback

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/events"
)

// Limits of what people write, in characters (the contract's maxLength).
const (
	maxTitle   = 80
	maxBody    = 5000
	maxReason  = 1000
	maxComment = 5000
	maxNote    = 500
)

// text trims v and checks it is there and not longer than most.
func text(fields *[]FieldError, field, v string, most int, required bool) string {
	v = strings.TrimSpace(v)
	switch {
	case v == "" && required:
		*fields = append(*fields, FieldError{Field: field, Code: "required"})
	case utf8.RuneCountInString(v) > most:
		*fields = append(*fields, FieldError{Field: field, Code: "too_long"})
	}
	return v
}

func validSeverity(s Severity) bool {
	return s == SeverityLow || s == SeverityMedium || s == SeverityHigh
}

// Create submits feedback. Where it came from (web, command line, AI-drafted) is decided from
// the actor and n.AIDrafted, and the context keeps only the fields that entry point carries.
func (s *Service) Create(ctx context.Context, a access.Actor, n Submission) (Feedback, error) {
	var fields []FieldError
	switch n.Type {
	case TypeBug, TypeSuggestion, TypeQuestion:
	default:
		fields = append(fields, FieldError{Field: "type", Code: "enum"})
	}
	n.Title = text(&fields, "title", n.Title, maxTitle, true)
	n.Body = text(&fields, "body", n.Body, maxBody, true)
	if n.Severity == "" {
		n.Severity = SeverityMedium
	} else if !validSeverity(n.Severity) {
		fields = append(fields, FieldError{Field: "severity", Code: "enum"})
	}
	origin, via := OriginWeb, viaWeb
	switch a.Origin {
	case access.OriginCLI:
		origin, via = OriginCLI, viaCLI
	case access.OriginSystem:
		origin, via = OriginSystem, viaSystem
	}
	if n.AIDrafted {
		origin = OriginAI
	}
	clean, err := cleanContext(n.Context, via)
	var ce *Error
	if errors.As(err, &ce) {
		fields = append(fields, ce.Fields...)
	}
	var resourceID string
	if n.Resource != "" {
		b, found, err := s.res.BriefByCode(ctx, n.Resource)
		if err != nil {
			return Feedback{}, err
		}
		if found {
			// Feedback about a resource one cannot see would tell its handlers nothing useful,
			// and the answer would tell the submitter it exists.
			d := s.access.Can(ctx, a, access.ActionResourceRead, b.Target)
			if d.Err != nil {
				return Feedback{}, d.Err
			}
			found = d.Allowed
		}
		switch {
		case !found:
			fields = append(fields, FieldError{Field: "resource", Code: "unknown"})
		case n.ResourceVersion < 0 || n.ResourceVersion > b.Version:
			fields = append(fields, FieldError{Field: "resource_version", Code: "unknown"})
		default:
			resourceID = b.ID
		}
	} else if n.ResourceVersion != 0 {
		fields = append(fields, FieldError{Field: "resource_version", Code: "without_resource"})
	}
	if len(fields) > 0 {
		return Feedback{}, invalid(fields...)
	}
	ctxJSON, err := json.Marshal(clean)
	if err != nil {
		return Feedback{}, err
	}

	var out Feedback
	err = s.write(ctx, a, func(o *op) error {
		obj := object(0)
		obj.ID, obj.Label = "new", "new feedback"
		if err := o.check(access.ActionFeedbackCreate, access.TargetPlatform(), obj); err != nil {
			return err
		}
		// Numbers are shown to people (FB-42): one after another, never reused. The lock
		// serializes submissions on PostgreSQL; SQLite has one writer anyway.
		if err := o.tx.Lock(ctx, "feedback.number"); err != nil {
			return err
		}
		var number int
		if err := o.tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number), 0) + 1 FROM feedback`).Scan(&number); err != nil {
			return err
		}
		id, now := dbx.NewID(), s.millis()
		if _, err := o.tx.ExecContext(ctx, `INSERT INTO feedback (id, number, kind, origin, status, severity, title, body, author_id,
			resource_id, resource_version, context, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, number, string(n.Type), string(origin), string(StatusNew), string(n.Severity), n.Title, n.Body, nullable(a.UserID),
			nullable(resourceID), nullInt(n.ResourceVersion), string(ctxJSON), now, now); err != nil {
			return err
		}
		if err := addEvent(ctx, o.tx, id, Event{Kind: EventCreated, ActorID: a.UserID, At: dbx.Time(now)}); err != nil {
			return err
		}
		change := map[string]any{"type": string(n.Type), "origin": string(origin)}
		if n.Resource != "" {
			change["resource"] = n.Resource
		}
		if err := o.record(access.ActionFeedbackCreate, object(number), change); err != nil {
			return err
		}
		r, err := s.one(ctx, o.tx, number)
		if err != nil {
			return err
		}
		out = r.Feedback
		out.Events, err = s.timeline(ctx, o.tx, id)
		return err
	})
	return out, err
}

// Get returns feedback with its timeline, if a may see it.
func (s *Service) Get(ctx context.Context, a access.Actor, number int) (Feedback, error) {
	r, err := s.one(ctx, s.db, number)
	if err != nil {
		return Feedback{}, err
	}
	ok, err := s.sees(ctx, s.db, a, r.Feedback, r.target)
	if err != nil {
		return Feedback{}, err
	}
	if !ok {
		return Feedback{}, ErrNotFound
	}
	out := r.Feedback
	out.Events, err = s.timeline(ctx, s.db, r.ID)
	return out, err
}

// MaxLimit is the largest page List returns.
const MaxLimit = 200

// visible returns the condition on f for the feedback a may see: what they submitted or are
// assigned, feedback about the resources they handle, and, for whoever handles the platform
// (administrators), feedback about no resource.
func (s *Service) visible(ctx context.Context, a access.Actor) (string, []any, error) {
	if a.IsSystem() {
		return "1 = 1", nil, nil
	}
	conds := []string{"f.author_id = ?", "f.assignee_id = ?"}
	args := []any{a.UserID, a.UserID}
	platform, err := s.handles(ctx, s.db, a, access.TargetPlatform())
	if err != nil {
		return "", nil, err
	}
	if platform {
		conds = append(conds, "f.resource_id IS NULL")
	}
	// The resources that have feedback, filtered by who handles them. There are few of them,
	// and a decision per resource keeps the rule in one place (access).
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT resource_id FROM feedback WHERE resource_id IS NOT NULL`)
	if err != nil {
		return "", nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	briefs, err := s.res.Briefs(ctx, ids)
	if err != nil {
		return "", nil, err
	}
	var mine []any
	for _, id := range ids {
		t := access.TargetPlatform() // a resource that is gone: the platform's
		if b, ok := briefs[id]; ok {
			t = b.Target
		}
		ok, err := s.handles(ctx, s.db, a, t)
		if err != nil {
			return "", nil, err
		}
		if ok {
			mine = append(mine, id)
		}
	}
	if len(mine) > 0 {
		conds = append(conds, "f.resource_id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(mine)), ",")+")")
		args = append(args, mine...)
	}
	return "(" + strings.Join(conds, " OR ") + ")", args, nil
}

// filter adds f's conditions to a visibility condition. ok is false when nothing can match.
func (s *Service) filter(ctx context.Context, a access.Actor, f Filter, where string, args []any) (string, []any, bool, error) {
	if f.Status != "" {
		where, args = where+" AND f.status = ?", append(args, string(f.Status))
	}
	if f.Type != "" {
		where, args = where+" AND f.kind = ?", append(args, string(f.Type))
	}
	if f.Mine {
		where, args = where+" AND f.author_id = ?", append(args, a.UserID)
	}
	if f.AssignedToMe {
		where, args = where+" AND f.assignee_id = ?", append(args, a.UserID)
	}
	if f.Resource != "" {
		b, found, err := s.res.BriefByCode(ctx, f.Resource)
		if err != nil || !found {
			return "", nil, false, err
		}
		where, args = where+" AND f.resource_id = ?", append(args, b.ID)
	}
	return where, args, true, nil
}

// List returns the feedback a may see, most recently updated first. cursor is the previous
// page's next cursor; the returned one is "" on the last page.
func (s *Service) List(ctx context.Context, a access.Actor, f Filter, limit int, cursor string) ([]Feedback, string, error) {
	if limit <= 0 || limit > MaxLimit {
		limit = 50
	}
	where, args, err := s.visible(ctx, a)
	if err != nil {
		return nil, "", err
	}
	where, args, ok, err := s.filter(ctx, a, f, where, args)
	if err != nil || !ok {
		return nil, "", err
	}
	if cursor != "" {
		at, id, ok := decodeCursor(cursor)
		if !ok {
			return nil, "", invalid(FieldError{Field: "cursor", Code: "invalid"})
		}
		where += " AND (f.updated_at < ? OR (f.updated_at = ? AND f.id < ?))"
		args = append(args, at, at, id)
	}
	rs, err := s.load(ctx, s.db, where, "f.updated_at DESC, f.id DESC", limit+1, args...)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rs) > limit {
		rs = rs[:limit]
		last := rs[limit-1]
		next = encodeCursor(dbx.Millis(last.UpdatedAt), last.ID)
	}
	out := make([]Feedback, len(rs))
	for i, r := range rs {
		out[i] = r.Feedback
	}
	return out, next, nil
}

func encodeCursor(at int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at, 10) + "." + id))
}

func decodeCursor(c string) (int64, string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, "", false
	}
	at, id, ok := strings.Cut(string(raw), ".")
	n, err := strconv.ParseInt(at, 10, 64)
	return n, id, ok && err == nil && id != ""
}

// Summary counts the feedback a may see by status; with mine, only what they submitted.
func (s *Service) Summary(ctx context.Context, a access.Actor, mine bool) (Summary, error) {
	out := Summary{ByStatus: map[Status]int{}}
	for _, st := range Statuses {
		out.ByStatus[st] = 0
	}
	where, args, err := s.visible(ctx, a)
	if err != nil {
		return out, err
	}
	if mine {
		where, args = where+" AND f.author_id = ?", append(args, a.UserID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.status, COUNT(*) FROM feedback f WHERE `+where+` GROUP BY f.status`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return out, err
		}
		out.ByStatus[Status(st)] = n
		if !Status(st).Closed() {
			out.Open += n
		}
	}
	return out, rows.Err()
}

// Update changes the status, the assignee or the severity: for whoever handles the feedback.
// wont_fix needs a reason. A status change on feedback nobody is assigned to assigns whoever
// made it, so that there is a handler to tell when the submitter reopens it. Status changes go
// on the timeline and are published (events.FeedbackStatusChanged) for notify to tell the
// submitter.
func (s *Service) Update(ctx context.Context, a access.Actor, number int, c Change) (Feedback, error) {
	var out Feedback
	err := s.write(ctx, a, func(o *op) error {
		r, err := s.one(ctx, o.tx, number)
		if err != nil {
			return err
		}
		seen, err := s.sees(ctx, o.tx, a, r.Feedback, r.target)
		if err != nil {
			return err
		}
		if err := o.check(access.ActionFeedbackManage, r.target, object(number)); err != nil {
			if !seen {
				return ErrNotFound
			}
			return err
		}

		var fields []FieldError
		next := r.Feedback
		if c.Severity != nil {
			if !validSeverity(*c.Severity) {
				fields = append(fields, FieldError{Field: "severity", Code: "enum"})
			}
			next.Severity = *c.Severity
		}
		reason := ""
		if c.StatusReason != nil {
			reason = text(&fields, "status_reason", *c.StatusReason, maxReason, false)
		}
		if c.Status != nil && *c.Status != r.Status {
			switch *c.Status {
			case StatusAccepted, StatusInProgress, StatusResolved:
			case StatusWontFix:
				if reason == "" {
					return ErrReasonRequired
				}
			default:
				// New is where feedback starts, not a place to send it back to.
				fields = append(fields, FieldError{Field: "status", Code: "enum"})
			}
			next.Status = *c.Status
		}
		switch {
		case c.Unassign && c.Assignee != nil:
			fields = append(fields, FieldError{Field: "unassign", Code: "conflict"})
		case c.Unassign:
			next.AssigneeID = ""
		case c.Assignee != nil:
			// Only to someone who can handle it: anyone else could not even open it.
			ok, err := s.handles(ctx, o.tx, access.Actor{UserID: *c.Assignee, Origin: access.OriginWeb}, r.target)
			if err != nil {
				return err
			}
			if !ok {
				fields = append(fields, FieldError{Field: "assignee", Code: "not_handler"})
			}
			next.AssigneeID = *c.Assignee
		case next.Status != r.Status && r.AssigneeID == "" && a.UserID != "":
			next.AssigneeID = a.UserID
		}
		if len(fields) > 0 {
			return invalid(fields...)
		}

		now := s.millis()
		at := dbx.Time(now)
		change := map[string]any{}
		if next.Severity != r.Severity {
			change["severity"] = map[string]any{"from": string(r.Severity), "to": string(next.Severity)}
		}
		if next.AssigneeID != r.AssigneeID {
			change["assignee"] = map[string]any{"from": r.AssigneeID, "to": next.AssigneeID}
			if err := addEvent(ctx, o.tx, r.ID, Event{Kind: EventAssigned, ActorID: a.UserID, AssigneeID: next.AssigneeID, At: at}); err != nil {
				return err
			}
		}
		statusChanged := next.Status != r.Status
		if statusChanged {
			change["status"] = map[string]any{"from": string(r.Status), "to": string(next.Status)}
			if err := addEvent(ctx, o.tx, r.ID, Event{Kind: EventStatus, ActorID: a.UserID, Status: next.Status, Body: reason, At: at}); err != nil {
				return err
			}
			next.StatusReason = reason
		}
		if len(change) == 0 {
			out = r.Feedback
			out.Events, err = s.timeline(ctx, o.tx, r.ID)
			return err
		}
		q := `UPDATE feedback SET severity = ?, assignee_id = ?, status = ?, status_reason = ?, updated_at = ?`
		args := []any{string(next.Severity), nullable(next.AssigneeID), string(next.Status), next.StatusReason, now}
		switch {
		case statusChanged && next.Status.Closed():
			q, args = q+`, closed_at = ?`, append(args, now)
		case statusChanged:
			q += `, closed_at = NULL`
		}
		if statusChanged && next.Status == StatusResolved {
			// A new resolution waits for a new score.
			q += `, score = NULL, score_note = ''`
		}
		if _, err := o.tx.ExecContext(ctx, q+` WHERE id = ?`, append(args, r.ID)...); err != nil {
			return err
		}
		if err := o.record(access.ActionFeedbackManage, object(number), change); err != nil {
			return err
		}
		updated, err := s.one(ctx, o.tx, number)
		if err != nil {
			return err
		}
		if statusChanged {
			o.emit(events.FeedbackStatusChanged{FeedbackRef: s.refOf(updated), Old: string(r.Status), New: string(next.Status), ActorID: a.UserID})
		}
		out = updated.Feedback
		out.Events, err = s.timeline(ctx, o.tx, r.ID)
		return err
	})
	return out, err
}

// Comment adds a reply to the timeline: anyone who may see the feedback may reply. It is
// published (events.FeedbackCommented) without its text.
func (s *Service) Comment(ctx context.Context, a access.Actor, number int, body string) (Event, error) {
	var fields []FieldError
	body = text(&fields, "body", body, maxComment, true)
	if len(fields) > 0 {
		return Event{}, invalid(fields...)
	}
	var out Event
	err := s.db.InTx(ctx, func(tx *dbx.Tx) error {
		r, err := s.one(ctx, tx, number)
		if err != nil {
			return err
		}
		ok, err := s.sees(ctx, tx, a, r.Feedback, r.target)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		now := s.millis()
		out = Event{Kind: EventComment, ActorID: a.UserID, Body: body, At: dbx.Time(now)}
		if err := addEvent(ctx, tx, r.ID, out); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE feedback SET updated_at = ? WHERE id = ?`, now, r.ID); err != nil {
			return err
		}
		evs, err := s.timeline(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		out = evs[len(evs)-1]
		if s.events != nil {
			s.events.Publish(tx, events.FeedbackCommented{FeedbackRef: s.refOf(r), ActorID: a.UserID})
		}
		return nil
	})
	return out, err
}

// LowScore is the highest score that reopens resolved feedback.
const LowScore = 2

// Rate records the submitter's score for resolved feedback. A score of LowScore or less reopens
// it (back to accepted) and is published as a status change, so that its handler hears of it.
func (s *Service) Rate(ctx context.Context, a access.Actor, number, score int, note string) (Feedback, error) {
	var fields []FieldError
	if score < 1 || score > 5 {
		fields = append(fields, FieldError{Field: "score", Code: "range"})
	}
	note = text(&fields, "note", note, maxNote, false)
	if len(fields) > 0 {
		return Feedback{}, invalid(fields...)
	}
	var out Feedback
	err := s.db.InTx(ctx, func(tx *dbx.Tx) error {
		r, err := s.one(ctx, tx, number)
		if err != nil {
			return err
		}
		if a.UserID == "" || a.UserID != r.SubmitterID {
			ok, err := s.sees(ctx, tx, a, r.Feedback, r.target)
			if err != nil {
				return err
			}
			if !ok {
				return ErrNotFound
			}
			return ErrDenied
		}
		if r.Status != StatusResolved {
			return ErrNotResolved
		}
		now := s.millis()
		at := dbx.Time(now)
		if err := addEvent(ctx, tx, r.ID, Event{Kind: EventRated, ActorID: a.UserID, Score: score, Body: note, At: at}); err != nil {
			return err
		}
		q := `UPDATE feedback SET score = ?, score_note = ?, updated_at = ?`
		args := []any{score, note, now}
		reopen := score <= LowScore
		if reopen {
			q, args = q+`, status = ?, status_reason = '', closed_at = NULL`, append(args, string(StatusAccepted))
			if err := addEvent(ctx, tx, r.ID, Event{Kind: EventReopened, ActorID: a.UserID, Status: StatusAccepted, At: at}); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, q+` WHERE id = ?`, append(args, r.ID)...); err != nil {
			return err
		}
		updated, err := s.one(ctx, tx, number)
		if err != nil {
			return err
		}
		if reopen && s.events != nil {
			s.events.Publish(tx, events.FeedbackStatusChanged{FeedbackRef: s.refOf(updated), Old: string(StatusResolved), New: string(StatusAccepted), ActorID: a.UserID})
		}
		out = updated.Feedback
		out.Events, err = s.timeline(ctx, tx, r.ID)
		return err
	})
	return out, err
}

// refOf is the part of every event about r.
func (s *Service) refOf(r row) events.FeedbackRef {
	ref := s.ref(r.Feedback)
	ref.ProductID = r.product
	return ref
}
