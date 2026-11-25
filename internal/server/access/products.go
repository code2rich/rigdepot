// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Product is a group of people and the resources only they get.
type Product struct {
	ID          string
	Code        string // permanent; the first word of product commands
	Name        string
	Line        string // product line, a grouping label
	Owners      []UserRef
	MemberCount int  // owners and members
	MyRole      Role // the actor's role in it; empty when they are not in it
	CreatedAt   time.Time
}

// Ref returns the short form of p.
func (p Product) Ref() ProductRef { return ProductRef{ID: p.ID, Code: p.Code, Name: p.Name} }

func productObject(p ProductRef) object { return object{kind: "product", id: p.ID, label: p.Code} }

// productByCode reads one product without its owners; ErrNotFound when there
// is none.
func productByCode(ctx context.Context, q dbx.Querier, code string) (Product, error) {
	var p Product
	var created int64
	err := q.QueryRowContext(ctx, `SELECT id, code, name, product_line, created_at FROM products WHERE code = ?`, code).
		Scan(&p.ID, &p.Code, &p.Name, &p.Line, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound.with(map[string]any{"product": code})
	}
	p.CreatedAt = dbx.Time(created)
	return p, err
}

// listProducts reads products with their owners and member counts, by name.
// With code set it reads only that one.
func listProducts(ctx context.Context, q dbx.Querier, actorID, code string) ([]Product, error) {
	query := `SELECT p.id, p.code, p.name, p.product_line, p.created_at,
		(SELECT COUNT(*) FROM memberships m WHERE m.product_id = p.id),
		COALESCE((SELECT m.role FROM memberships m WHERE m.product_id = p.id AND m.user_id = ?), '')
		FROM products p`
	args := []any{actorID}
	if code != "" {
		query += ` WHERE p.code = ?`
		args = append(args, code)
	}
	query += ` ORDER BY LOWER(p.name), p.code`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out []Product
	index := map[string]int{}
	for rows.Next() {
		var p Product
		var created int64
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.Line, &created, &p.MemberCount, &p.MyRole); err != nil {
			rows.Close()
			return nil, err
		}
		p.CreatedAt = dbx.Time(created)
		p.Owners = []UserRef{}
		index[p.ID] = len(out)
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ownerQuery := `SELECT m.product_id, u.id, u.username, u.display_name
		FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.role = 'owner'`
	var ownerArgs []any
	if code != "" {
		if len(out) == 0 {
			return out, nil
		}
		ownerQuery += ` AND m.product_id = ?`
		ownerArgs = append(ownerArgs, out[0].ID)
	}
	ownerQuery += ` ORDER BY LOWER(u.display_name), u.username`
	rows, err = q.QueryContext(ctx, ownerQuery, ownerArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid string
		var u UserRef
		if err := rows.Scan(&pid, &u.ID, &u.Username, &u.DisplayName); err != nil {
			return nil, err
		}
		if i, ok := index[pid]; ok {
			out[i].Owners = append(out[i].Owners, u)
		}
	}
	return out, rows.Err()
}

func getProduct(ctx context.Context, q dbx.Querier, actorID, code string) (Product, error) {
	ps, err := listProducts(ctx, q, actorID, code)
	if err != nil {
		return Product{}, err
	}
	if len(ps) == 0 {
		return Product{}, ErrNotFound.with(map[string]any{"product": code})
	}
	return ps[0], nil
}

// ListProducts returns every product, by name. Everyone signed in may list
// them, including people choosing what to ask to join.
func (s *Service) ListProducts(ctx context.Context, a Actor) ([]Product, error) {
	if err := s.read(ctx, a, ActionProductRead, TargetPlatform()); err != nil {
		return nil, err
	}
	return listProducts(ctx, s.db, a.UserID, "")
}

// GetProduct returns one product.
func (s *Service) GetProduct(ctx context.Context, a Actor, code string) (Product, error) {
	if err := s.read(ctx, a, ActionProductRead, TargetPlatform()); err != nil {
		return Product{}, err
	}
	return getProduct(ctx, s.db, a.UserID, code)
}

// NewProduct is a product to create.
type NewProduct struct {
	Code   string
	Name   string
	Line   string
	Owners []string // user ids; may be empty and filled in later
}

// CreateProduct creates a product. Only platform administrators may. The
// code is permanent and cannot be a reserved word (ErrCodeReserved) or one
// that is taken (ErrCodeTaken).
func (s *Service) CreateProduct(ctx context.Context, a Actor, n NewProduct) (Product, error) {
	fields := []*FieldError{checkProductCode("code", n.Code), checkText("name", n.Name, 1, 64), checkText("line", n.Line, 0, 64)}
	if len(n.Owners) > 16 {
		fields = append(fields, &FieldError{"owners", "too_many"})
	}
	for i, id := range n.Owners {
		fields = append(fields, checkID(fmt.Sprintf("owners[%d]", i), id))
	}
	if err := collect(fields...); err != nil {
		return Product{}, err
	}
	owners := dedupe(n.Owners)
	var out Product
	err := s.write(ctx, a, func(op *writeOp) error {
		obj := object{kind: "product", label: n.Code}
		if err := op.check(ActionProductCreate, TargetPlatform(), obj); err != nil {
			return err
		}
		if slices.Contains(reservedProductCodes, n.Code) {
			return ErrCodeReserved.with(map[string]any{"code": n.Code})
		}
		if _, err := productByCode(ctx, op.tx, n.Code); err == nil {
			return ErrCodeTaken.with(map[string]any{"code": n.Code})
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		for i, id := range owners {
			u, err := getUser(ctx, op.tx, id)
			if errors.Is(err, ErrNotFound) {
				return invalid(FieldError{fmt.Sprintf("owners[%d]", i), "unknown"})
			}
			if err != nil {
				return err
			}
			if u.Status == StatusDisabled {
				return ErrUserDisabled.with(map[string]any{"user": u.Username})
			}
		}
		now := s.millis()
		p := ProductRef{ID: dbx.NewID(), Code: n.Code, Name: n.Name}
		if _, err := op.tx.ExecContext(ctx, `INSERT INTO products (id, code, name, product_line, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			p.ID, p.Code, p.Name, n.Line, now, now); err != nil {
			if dbx.IsUniqueViolation(err) {
				return ErrCodeTaken.with(map[string]any{"code": n.Code})
			}
			return err
		}
		if err := op.record(ActionProductCreate, productObject(p), map[string]any{"code": p.Code, "name": p.Name, "line": n.Line}); err != nil {
			return err
		}
		op.emit(ProductCreated{ProductID: p.ID, Code: p.Code})
		for _, id := range owners {
			if _, err := s.setRole(op, p, id, RoleOwner); err != nil {
				return err
			}
		}
		var err error
		out, err = getProduct(ctx, op.tx, a.UserID, p.Code)
		return err
	})
	return out, err
}

// ProductChange is what can change about a product. Nil fields stay; the
// code never changes.
type ProductChange struct {
	Name *string
	Line *string
}

// UpdateProduct renames a product or changes its product line.
func (s *Service) UpdateProduct(ctx context.Context, a Actor, code string, c ProductChange) (Product, error) {
	var fields []*FieldError
	if c.Name != nil {
		fields = append(fields, checkText("name", *c.Name, 1, 64))
	}
	if c.Line != nil {
		fields = append(fields, checkText("line", *c.Line, 0, 64))
	}
	if err := collect(fields...); err != nil {
		return Product{}, err
	}
	var out Product
	err := s.write(ctx, a, func(op *writeOp) error {
		p, err := productByCode(ctx, op.tx, code)
		if err != nil {
			return err
		}
		if err := op.check(ActionProductUpdate, TargetProduct(p.ID), productObject(p.Ref())); err != nil {
			return err
		}
		change := map[string]any{}
		if c.Name != nil && *c.Name != p.Name {
			change["name"] = map[string]any{"from": p.Name, "to": *c.Name}
			p.Name = *c.Name
		}
		if c.Line != nil && *c.Line != p.Line {
			change["line"] = map[string]any{"from": p.Line, "to": *c.Line}
			p.Line = *c.Line
		}
		if len(change) > 0 {
			if _, err := op.tx.ExecContext(ctx, `UPDATE products SET name = ?, product_line = ?, updated_at = ? WHERE id = ?`,
				p.Name, p.Line, s.millis(), p.ID); err != nil {
				return err
			}
			if err := op.record(ActionProductUpdate, productObject(p.Ref()), change); err != nil {
				return err
			}
		}
		out, err = getProduct(ctx, op.tx, a.UserID, code)
		return err
	})
	return out, err
}

// Member is someone in a product.
type Member struct {
	User        UserRef
	Role        Role
	Job         string
	FocusStages []string
	AddedAt     time.Time
}

const memberColumns = `u.id, u.username, u.display_name, u.job_title, u.stages, m.role, m.created_at`

func scanMember(sc interface{ Scan(...any) error }) (Member, error) {
	var m Member
	var stages string
	var added int64
	if err := sc.Scan(&m.User.ID, &m.User.Username, &m.User.DisplayName, &m.Job, &stages, &m.Role, &added); err != nil {
		return m, err
	}
	m.AddedAt = dbx.Time(added)
	m.FocusStages = []string{}
	if err := json.Unmarshal([]byte(stages), &m.FocusStages); err != nil {
		return m, fmt.Errorf("access: user %s: stages: %w", m.User.ID, err)
	}
	return m, nil
}

// ListMembers returns a product's owners and members: owners first, then by
// name. Its members, owners and platform administrators may see them.
func (s *Service) ListMembers(ctx context.Context, a Actor, code string) ([]Member, error) {
	p, err := productByCode(ctx, s.db, code)
	if err != nil {
		return nil, err
	}
	if err := s.read(ctx, a, ActionMemberRead, TargetProduct(p.ID)); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+memberColumns+`
		FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.product_id = ?
		ORDER BY CASE m.role WHEN 'owner' THEN 0 ELSE 1 END, LOWER(u.display_name), u.username`, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// productLock returns the lock that serialises every change that can take
// away a product's last owner.
func productLock(productID string) string { return "access.product:" + productID }

// PutMember adds someone to a product or changes their role in it, and
// reports whether they were added. Platform administrators and the
// product's owners may. Adding someone whose account is waiting to be
// activated activates it. A product that has owners keeps at least one
// (ErrLastOwner).
func (s *Service) PutMember(ctx context.Context, a Actor, code, userID string, role Role) (m Member, added bool, err error) {
	if role != RoleOwner && role != RoleMember {
		return Member{}, false, invalid(FieldError{"role", "enum"})
	}
	if fe := checkID("user_id", userID); fe != nil {
		return Member{}, false, invalid(*fe)
	}
	err = s.write(ctx, a, func(op *writeOp) error {
		p, err := productByCode(ctx, op.tx, code)
		if err != nil {
			return err
		}
		obj := object{kind: "membership", id: p.ID + "/" + userID, label: p.Code}
		var current Role
		err = op.tx.QueryRowContext(ctx, `SELECT role FROM memberships WHERE product_id = ? AND user_id = ?`, p.ID, userID).Scan(&current)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		action := ActionMemberUpdate
		if current == "" {
			action = ActionMemberAdd
		}
		if err := op.check(action, TargetProduct(p.ID), obj); err != nil {
			return err
		}
		if err := op.tx.Lock(ctx, productLock(p.ID)); err != nil {
			return err
		}
		added, err = s.setRole(op, p.Ref(), userID, role)
		if err != nil {
			return err
		}
		m, err = scanMember(op.tx.QueryRowContext(ctx, `SELECT `+memberColumns+`
			FROM memberships m JOIN users u ON u.id = m.user_id
			WHERE m.product_id = ? AND m.user_id = ?`, p.ID, userID))
		return err
	})
	return m, added, err
}

// setRole gives userID role in p and records it; the permission check is the
// caller's. It refuses to demote the last owner, so the caller holds
// productLock when it can demote.
func (s *Service) setRole(op *writeOp, p ProductRef, userID string, role Role) (added bool, err error) {
	ctx := op.ctx
	u, err := getUser(ctx, op.tx, userID)
	if errors.Is(err, ErrNotFound) {
		return false, ErrNotFound.with(map[string]any{"user": userID})
	}
	if err != nil {
		return false, err
	}
	if u.Status == StatusDisabled {
		return false, ErrUserDisabled.with(map[string]any{"user": u.Username})
	}
	var current Role
	for _, m := range u.Memberships {
		if m.Product.ID == p.ID {
			current = m.Role
		}
	}
	obj := object{kind: "membership", id: p.ID + "/" + userID, label: p.Code + "/" + u.Username}
	switch current {
	case role:
		return false, nil
	case "":
		if _, err := op.tx.ExecContext(ctx, `INSERT INTO memberships (product_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
			p.ID, userID, string(role), s.millis()); err != nil {
			return false, err
		}
		if err := op.record(ActionMemberAdd, obj, map[string]any{"role": role}); err != nil {
			return false, err
		}
	default:
		if current == RoleOwner {
			if err := lastOwnerGuard(op, p, userID); err != nil {
				return false, err
			}
		}
		if _, err := op.tx.ExecContext(ctx, `UPDATE memberships SET role = ? WHERE product_id = ? AND user_id = ?`,
			string(role), p.ID, userID); err != nil {
			return false, err
		}
		if err := op.record(ActionMemberUpdate, obj, map[string]any{"role": map[string]any{"from": current, "to": role}}); err != nil {
			return false, err
		}
	}
	op.emit(MembershipChanged{ProductID: p.ID, UserID: userID, Old: current, New: role})
	if u.Status == StatusPending {
		if err := s.activate(op, u); err != nil {
			return false, err
		}
	}
	return current == "", nil
}

// lastOwnerGuard returns ErrLastOwner when userID is the only owner of p.
func lastOwnerGuard(op *writeOp, p ProductRef, userID string) error {
	var others int
	if err := op.tx.QueryRowContext(op.ctx, `SELECT COUNT(*) FROM memberships WHERE product_id = ? AND role = 'owner' AND user_id <> ?`,
		p.ID, userID).Scan(&others); err != nil {
		return err
	}
	if others == 0 {
		return ErrLastOwner.with(map[string]any{"product": p.Code})
	}
	return nil
}

// activate turns a pending account into an active one: the first product
// someone gets into lets them use the platform.
func (s *Service) activate(op *writeOp, u User) error {
	if _, err := op.tx.ExecContext(op.ctx, `UPDATE users SET status = 'active', updated_at = ? WHERE id = ? AND status = 'pending'`,
		s.millis(), u.ID); err != nil {
		return err
	}
	if err := op.record(ActionUserEnable, userObject(u), map[string]any{"status": map[string]any{"from": StatusPending, "to": StatusActive}}); err != nil {
		return err
	}
	op.emit(UserStatusChanged{UserID: u.ID, Old: StatusPending, New: StatusActive})
	return nil
}

// RemoveMember takes someone out of a product. Platform administrators and
// the product's owners may. Their next request no longer sees the product,
// and its resources leave their computers at the next sync. The last owner
// cannot be removed (ErrLastOwner).
func (s *Service) RemoveMember(ctx context.Context, a Actor, code, userID string) error {
	if fe := checkID("user_id", userID); fe != nil {
		return invalid(*fe)
	}
	return s.write(ctx, a, func(op *writeOp) error {
		p, err := productByCode(ctx, op.tx, code)
		if err != nil {
			return err
		}
		obj := object{kind: "membership", id: p.ID + "/" + userID, label: p.Code}
		if err := op.check(ActionMemberRemove, TargetProduct(p.ID), obj); err != nil {
			return err
		}
		if err := op.tx.Lock(ctx, productLock(p.ID)); err != nil {
			return err
		}
		var current Role
		var username string
		err = op.tx.QueryRowContext(ctx, `SELECT m.role, u.username FROM memberships m JOIN users u ON u.id = m.user_id
			WHERE m.product_id = ? AND m.user_id = ?`, p.ID, userID).Scan(&current, &username)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound.with(map[string]any{"product": code, "user": userID})
		}
		if err != nil {
			return err
		}
		if current == RoleOwner {
			if err := lastOwnerGuard(op, p.Ref(), userID); err != nil {
				return err
			}
		}
		if _, err := op.tx.ExecContext(ctx, `DELETE FROM memberships WHERE product_id = ? AND user_id = ?`, p.ID, userID); err != nil {
			return err
		}
		obj.label = p.Code + "/" + username
		if err := op.record(ActionMemberRemove, obj, map[string]any{"role": current}); err != nil {
			return err
		}
		op.emit(MembershipChanged{ProductID: p.ID, UserID: userID, Old: current})
		return nil
	})
}
