// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// JoinStatus is where a request to join a product stands.
type JoinStatus string

// The statuses the API shows. The table also allows "withdrawn", which no
// operation sets yet.
const (
	JoinPending  JoinStatus = "pending"
	JoinApproved JoinStatus = "approved"
	JoinRejected JoinStatus = "rejected"
)

// JoinRequest is someone asking to join a product.
type JoinRequest struct {
	ID          string
	User        UserRef
	Job         string
	FocusStages []string
	Product     ProductRef
	Note        string
	Status      JoinStatus
	CreatedAt   time.Time
	DecidedBy   *UserRef
	DecidedAt   *time.Time
	Reason      string // why it was rejected
}

const joinSelect = `SELECT j.id, u.id, u.username, u.display_name, u.job_title, u.stages,
	p.id, p.code, p.name, j.note, j.status, j.created_at,
	d.id, d.username, d.display_name, j.handled_at, j.reject_reason
	FROM join_requests j
	JOIN users u ON u.id = j.user_id
	JOIN products p ON p.id = j.product_id
	LEFT JOIN users d ON d.id = j.handled_by`

func scanJoin(sc interface{ Scan(...any) error }) (JoinRequest, error) {
	var j JoinRequest
	var stages string
	var created int64
	var dID, dName, dDisplay sql.NullString
	var handled sql.NullInt64
	if err := sc.Scan(&j.ID, &j.User.ID, &j.User.Username, &j.User.DisplayName, &j.Job, &stages,
		&j.Product.ID, &j.Product.Code, &j.Product.Name, &j.Note, &j.Status, &created,
		&dID, &dName, &dDisplay, &handled, &j.Reason); err != nil {
		return j, err
	}
	j.CreatedAt = dbx.Time(created)
	j.FocusStages = []string{}
	if err := json.Unmarshal([]byte(stages), &j.FocusStages); err != nil {
		return j, fmt.Errorf("access: user %s: stages: %w", j.User.ID, err)
	}
	if dID.Valid {
		j.DecidedBy = &UserRef{ID: dID.String, Username: dName.String, DisplayName: dDisplay.String}
	}
	if handled.Valid {
		t := dbx.Time(handled.Int64)
		j.DecidedAt = &t
	}
	return j, nil
}

func queryJoins(ctx context.Context, q dbx.Querier, where string, args ...any) ([]JoinRequest, error) {
	rows, err := q.QueryContext(ctx, joinSelect+` `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JoinRequest{}
	for rows.Next() {
		j, err := scanJoin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func joinObject(j JoinRequest) object {
	return object{kind: "join_request", id: j.ID, label: j.Product.Code + "/" + j.User.Username}
}

// userLock serialises one person's requests, so that asking twice at the
// same moment still leaves one pending request per product.
func userLock(userID string) string { return "access.user:" + userID }

// RequestToJoin asks for the actor to join each of the products, one request
// per product, each decided by that product's owners. Accounts that are not
// activated yet may ask: that is how they get in. Asking again while a
// request is pending returns the pending one. Asking to join a product one
// is already in is ErrAlreadyMember.
func (s *Service) RequestToJoin(ctx context.Context, a Actor, productCodes []string, note string) ([]JoinRequest, error) {
	fields := []*FieldError{checkText("note", note, 0, 500)}
	if len(productCodes) == 0 {
		fields = append(fields, &FieldError{"products", "required"})
	}
	if len(productCodes) > 16 {
		fields = append(fields, &FieldError{"products", "too_many"})
	}
	for i, c := range productCodes {
		fields = append(fields, checkProductCode(fmt.Sprintf("products[%d]", i), c))
	}
	if err := collect(fields...); err != nil {
		return nil, err
	}
	var out []JoinRequest
	err := s.write(ctx, a, func(op *writeOp) error {
		out = nil
		if err := op.tx.Lock(ctx, userLock(a.UserID)); err != nil {
			return err
		}
		for _, code := range dedupe(productCodes) {
			p, err := productByCode(ctx, op.tx, code)
			if err != nil {
				return err
			}
			obj := object{kind: "join_request", label: p.Code}
			if err := op.check(ActionJoinCreate, TargetProduct(p.ID), obj); err != nil {
				return err
			}
			var member int
			if err := op.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memberships WHERE product_id = ? AND user_id = ?`, p.ID, a.UserID).Scan(&member); err != nil {
				return err
			}
			if member > 0 {
				return ErrAlreadyMember.with(map[string]any{"product": p.Code})
			}
			pending, err := queryJoins(ctx, op.tx, `WHERE j.user_id = ? AND j.product_id = ? AND j.status = 'pending'`, a.UserID, p.ID)
			if err != nil {
				return err
			}
			if len(pending) > 0 {
				out = append(out, pending[0])
				continue
			}
			id := dbx.NewID()
			if _, err := op.tx.ExecContext(ctx, `INSERT INTO join_requests (id, user_id, product_id, note, status, created_at) VALUES (?, ?, ?, ?, 'pending', ?)`,
				id, a.UserID, p.ID, note, s.millis()); err != nil {
				return err
			}
			js, err := queryJoins(ctx, op.tx, `WHERE j.id = ?`, id)
			if err != nil {
				return err
			}
			if err := op.record(ActionJoinCreate, joinObject(js[0]), map[string]any{"product": p.Code}); err != nil {
				return err
			}
			op.emit(JoinRequested{RequestID: id, UserID: a.UserID, ProductID: p.ID})
			out = append(out, js[0])
		}
		return nil
	})
	return out, err
}

// MyJoinRequests returns the actor's own requests, newest first.
func (s *Service) MyJoinRequests(ctx context.Context, a Actor) ([]JoinRequest, error) {
	if err := s.read(ctx, a, ActionUserRead, TargetAccount(a.UserID)); err != nil {
		return nil, err
	}
	return queryJoins(ctx, s.db, `WHERE j.user_id = ? ORDER BY j.created_at DESC, j.id DESC`, a.UserID)
}

// JoinFilter narrows JoinRequests.
type JoinFilter struct {
	Product string     // a product code; empty for every product the actor decides on
	Status  JoinStatus // empty means pending
}

// JoinRequests returns the requests the actor may decide on, oldest first:
// all of them for platform administrators, those for their products for
// owners.
func (s *Service) JoinRequests(ctx context.Context, a Actor, f JoinFilter) ([]JoinRequest, error) {
	if f.Status == "" {
		f.Status = JoinPending
	}
	if f.Status != JoinPending && f.Status != JoinApproved && f.Status != JoinRejected {
		return nil, invalid(FieldError{"status", "enum"})
	}
	if f.Product != "" {
		p, err := productByCode(ctx, s.db, f.Product)
		if err != nil {
			return nil, err
		}
		if err := s.read(ctx, a, ActionJoinRead, TargetProduct(p.ID)); err != nil {
			return nil, err
		}
		return queryJoins(ctx, s.db, `WHERE j.product_id = ? AND j.status = ? ORDER BY j.created_at, j.id`, p.ID, string(f.Status))
	}
	all, err := queryJoins(ctx, s.db, `WHERE j.status = ? ORDER BY j.created_at, j.id`, string(f.Status))
	if err != nil {
		return nil, err
	}
	// Decide per product rather than per row: the answer only depends on it.
	allowed := map[string]bool{}
	out := []JoinRequest{}
	for _, j := range all {
		ok, seen := allowed[j.Product.ID]
		if !seen {
			d := s.Can(ctx, a, ActionJoinRead, TargetProduct(j.Product.ID))
			if d.Err != nil {
				return nil, d.Err
			}
			ok = d.Allowed
			allowed[j.Product.ID] = ok
		}
		if ok {
			out = append(out, j)
		}
	}
	return out, nil
}

// ApproveJoinRequest lets the person into the product as a member. The first
// approved request activates an account that was waiting.
func (s *Service) ApproveJoinRequest(ctx context.Context, a Actor, id string) (JoinRequest, error) {
	return s.decideJoin(ctx, a, id, true, "")
}

// RejectJoinRequest turns a request down. The reason is required: the person
// is told why.
func (s *Service) RejectJoinRequest(ctx context.Context, a Actor, id, reason string) (JoinRequest, error) {
	if err := collect(checkText("reason", reason, 1, 500)); err != nil {
		return JoinRequest{}, err
	}
	return s.decideJoin(ctx, a, id, false, reason)
}

func (s *Service) decideJoin(ctx context.Context, a Actor, id string, approve bool, reason string) (JoinRequest, error) {
	if fe := checkID("request_id", id); fe != nil {
		return JoinRequest{}, invalid(*fe)
	}
	action := ActionJoinReject
	if approve {
		action = ActionJoinApprove
	}
	var out JoinRequest
	err := s.write(ctx, a, func(op *writeOp) error {
		js, err := queryJoins(ctx, op.tx, `WHERE j.id = ?`, id)
		if err != nil {
			return err
		}
		if len(js) == 0 {
			return ErrNotFound
		}
		j := js[0]
		if err := op.check(action, TargetProduct(j.Product.ID), joinObject(j)); err != nil {
			return err
		}
		// Lock the person: approving changes their status and membership.
		if err := op.tx.Lock(ctx, userLock(j.User.ID)); err != nil {
			return err
		}
		var status JoinStatus
		if err := op.tx.QueryRowContext(ctx, `SELECT status FROM join_requests WHERE id = ?`, id).Scan(&status); err != nil {
			return err
		}
		if status != JoinPending {
			return ErrDecided.with(map[string]any{"status": status})
		}
		newStatus := JoinRejected
		if approve {
			newStatus = JoinApproved
		}
		var handler any
		if a.UserID != "" {
			handler = a.UserID
		}
		if _, err := op.tx.ExecContext(ctx, `UPDATE join_requests SET status = ?, handled_by = ?, handled_at = ?, reject_reason = ? WHERE id = ?`,
			string(newStatus), handler, s.millis(), reason, id); err != nil {
			return err
		}
		change := map[string]any{"status": map[string]any{"from": JoinPending, "to": newStatus}}
		if reason != "" {
			change["reason"] = reason
		}
		if err := op.record(action, joinObject(j), change); err != nil {
			return err
		}
		if approve {
			// Someone already in the product (added meanwhile) keeps their role.
			var current int
			if err := op.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memberships WHERE product_id = ? AND user_id = ?`, j.Product.ID, j.User.ID).Scan(&current); err != nil {
				return err
			}
			if current == 0 {
				if _, err := s.setRole(op, j.Product, j.User.ID, RoleMember); err != nil {
					return err
				}
			}
		}
		op.emit(JoinDecided{RequestID: id, UserID: j.User.ID, ProductID: j.Product.ID, Approved: approve, Reason: reason})
		js, err = queryJoins(ctx, op.tx, `WHERE j.id = ?`, id)
		if err != nil {
			return err
		}
		out = js[0]
		return nil
	})
	return out, err
}
