// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// InviteValidity is how long an invitation link works.
const InviteValidity = 7 * 24 * time.Hour

// Invite is an invitation to a local account.
type Invite struct {
	ID        string
	Email     string
	Products  []ProductRef // the products the account joins as a member
	CreatedBy UserRef
	CreatedAt time.Time
	ExpiresAt time.Time
	// Token is the secret part of the link. It is only known right after
	// the invitation is created; the database keeps its digest.
	Token string
}

// inviteDigest is what the database keeps of an invitation's token.
func inviteDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newInviteToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("access: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func inviteObject(id, email string) object { return object{kind: "invite", id: id, label: email} }

// CreateInvites invites people to local accounts: one invitation per
// address, each with a one-time link valid for InviteValidity. Only platform
// administrators may. An address that already has an account is
// ErrAccountExists; inviting an address again withdraws its earlier
// invitation, so only the newest link works.
func (s *Service) CreateInvites(ctx context.Context, a Actor, emails, productCodes []string) ([]Invite, error) {
	var fields []*FieldError
	if len(emails) == 0 {
		fields = append(fields, &FieldError{"emails", "required"})
	}
	if len(emails) > 100 {
		fields = append(fields, &FieldError{"emails", "too_many"})
	}
	var normal []string
	for i, e := range emails {
		n, ok := normalEmail(e)
		if !ok {
			fields = append(fields, &FieldError{fmt.Sprintf("emails[%d]", i), "format"})
		}
		normal = append(normal, n)
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
	normal = dedupe(normal)
	var out []Invite
	err := s.write(ctx, a, func(op *writeOp) error {
		out = nil
		if a.IsSystem() {
			// Every invitation names the administrator who sent it.
			return ErrDenied
		}
		if err := op.check(ActionInviteCreate, TargetPlatform(), object{kind: "invite", label: normal[0]}); err != nil {
			return err
		}
		var products []ProductRef
		for _, code := range dedupe(productCodes) {
			p, err := productByCode(ctx, op.tx, code)
			if err != nil {
				return err
			}
			products = append(products, p.Ref())
		}
		u, err := getUser(ctx, op.tx, a.UserID)
		if err != nil {
			return err
		}
		creator := u.Ref()
		now := s.now()
		for _, email := range normal {
			var accounts int
			if err := op.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE email = ?`, email).Scan(&accounts); err != nil {
				return err
			}
			if accounts > 0 {
				return ErrAccountExists.with(map[string]any{"email": email})
			}
			if _, err := op.tx.ExecContext(ctx, `UPDATE invites SET revoked_at = ? WHERE email = ? AND accepted_at IS NULL AND revoked_at IS NULL`,
				dbx.Millis(now), email); err != nil {
				return err
			}
			inv := Invite{
				ID: dbx.NewID(), Email: email, Products: products, CreatedBy: creator,
				CreatedAt: dbx.Time(dbx.Millis(now)), ExpiresAt: dbx.Time(dbx.Millis(now.Add(InviteValidity))),
				Token: newInviteToken(),
			}
			if inv.Products == nil {
				inv.Products = []ProductRef{}
			}
			if _, err := op.tx.ExecContext(ctx, `INSERT INTO invites (id, token_digest, email, role, invited_by, created_at, expires_at)
				VALUES (?, ?, ?, 'member', ?, ?, ?)`,
				inv.ID, inviteDigest(inv.Token), email, a.UserID, dbx.Millis(now), dbx.Millis(now.Add(InviteValidity))); err != nil {
				return err
			}
			codes := make([]string, 0, len(products))
			for _, p := range products {
				if _, err := op.tx.ExecContext(ctx, `INSERT INTO invite_products (invite_id, product_id) VALUES (?, ?)`, inv.ID, p.ID); err != nil {
					return err
				}
				codes = append(codes, p.Code)
			}
			if err := op.record(ActionInviteCreate, inviteObject(inv.ID, email), map[string]any{"email": email, "products": codes}); err != nil {
				return err
			}
			op.emit(InviteCreated{InviteID: inv.ID, Email: email})
			out = append(out, inv)
		}
		return nil
	})
	return out, err
}

const inviteSelect = `SELECT i.id, i.email, i.created_at, i.expires_at, i.accepted_at, i.revoked_at,
	u.id, u.username, u.display_name
	FROM invites i JOIN users u ON u.id = i.invited_by`

type inviteRow struct {
	Invite
	accepted, revoked bool
}

func queryInvites(ctx context.Context, q dbx.Querier, where string, args ...any) ([]inviteRow, error) {
	rows, err := q.QueryContext(ctx, inviteSelect+` `+where, args...)
	if err != nil {
		return nil, err
	}
	var out []inviteRow
	index := map[string]int{}
	for rows.Next() {
		var r inviteRow
		var created, expires int64
		var accepted, revoked sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Email, &created, &expires, &accepted, &revoked,
			&r.CreatedBy.ID, &r.CreatedBy.Username, &r.CreatedBy.DisplayName); err != nil {
			rows.Close()
			return nil, err
		}
		r.CreatedAt, r.ExpiresAt = dbx.Time(created), dbx.Time(expires)
		r.accepted, r.revoked = accepted.Valid, revoked.Valid
		r.Products = []ProductRef{}
		index[r.ID] = len(out)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	args = make([]any, 0, len(out))
	for _, r := range out {
		args = append(args, r.ID)
	}
	prows, err := q.QueryContext(ctx, `SELECT ip.invite_id, p.id, p.code, p.name
		FROM invite_products ip JOIN products p ON p.id = ip.product_id
		WHERE ip.invite_id IN (`+placeholders(len(args))+`) ORDER BY p.name, p.code`, args...)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var id string
		var p ProductRef
		if err := prows.Scan(&id, &p.ID, &p.Code, &p.Name); err != nil {
			return nil, err
		}
		out[index[id]].Products = append(out[index[id]].Products, p)
	}
	return out, prows.Err()
}

// ListInvites returns the invitations that can still be accepted, newest
// first. Only platform administrators may.
func (s *Service) ListInvites(ctx context.Context, a Actor) ([]Invite, error) {
	if err := s.read(ctx, a, ActionInviteRead, TargetPlatform()); err != nil {
		return nil, err
	}
	rows, err := queryInvites(ctx, s.db, `WHERE i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > ?
		ORDER BY i.created_at DESC, i.id DESC`, s.millis())
	if err != nil {
		return nil, err
	}
	out := make([]Invite, len(rows))
	for i, r := range rows {
		out[i] = r.Invite
	}
	return out, nil
}

// RevokeInvite withdraws an invitation: its link stops working. Only
// platform administrators may. Withdrawing one that is already used,
// withdrawn or expired changes nothing.
func (s *Service) RevokeInvite(ctx context.Context, a Actor, id string) error {
	if fe := checkID("invite_id", id); fe != nil {
		return invalid(*fe)
	}
	return s.write(ctx, a, func(op *writeOp) error {
		rows, err := queryInvites(ctx, op.tx, `WHERE i.id = ?`, id)
		if err != nil {
			return err
		}
		obj := inviteObject(id, id)
		if len(rows) > 0 {
			obj = inviteObject(id, rows[0].Email)
		}
		if err := op.check(ActionInviteRevoke, TargetPlatform(), obj); err != nil {
			return err
		}
		if len(rows) == 0 {
			return ErrNotFound
		}
		r := rows[0]
		if r.accepted || r.revoked || !s.now().Before(r.ExpiresAt) {
			return nil
		}
		if _, err := op.tx.ExecContext(ctx, `UPDATE invites SET revoked_at = ? WHERE id = ?`, s.millis(), id); err != nil {
			return err
		}
		return op.record(ActionInviteRevoke, obj, nil)
	})
}

// openInvite finds the invitation a link's token belongs to: ErrNotFound
// for a token that never was one, ErrInviteExpired for one that is used,
// withdrawn or expired.
func (s *Service) openInvite(ctx context.Context, q dbx.Querier, token string) (inviteRow, error) {
	if len(token) < 16 || len(token) > 128 {
		return inviteRow{}, ErrNotFound
	}
	rows, err := queryInvites(ctx, q, `WHERE i.token_digest = ?`, inviteDigest(token))
	if err != nil {
		return inviteRow{}, err
	}
	if len(rows) == 0 {
		return inviteRow{}, ErrNotFound
	}
	r := rows[0]
	if r.accepted || r.revoked || !s.now().Before(r.ExpiresAt) {
		return r, ErrInviteExpired
	}
	return r, nil
}

// PreviewInvite says what an invitation link is for, to the person holding
// it (who is not signed in).
func (s *Service) PreviewInvite(ctx context.Context, token string) (Invite, error) {
	r, err := s.openInvite(ctx, s.db, token)
	if err != nil {
		return Invite{}, err
	}
	r.CreatedBy = UserRef{}
	return r.Invite, nil
}

// NewAccount is what someone accepting an invitation chooses.
type NewAccount struct {
	Username    string
	DisplayName string
	Locale      string
}

// AcceptInvite creates the account an invitation is for, inside the
// caller's transaction: the sign-in code (package authn) sets the password
// and starts the session in the same transaction. The account is active,
// has the invitation's address (not counted as verified: the link may have
// been handed over rather than mailed) and is a member of the invitation's
// products. The link works once.
func (s *Service) AcceptInvite(ctx context.Context, tx *dbx.Tx, token string, n NewAccount) (User, error) {
	if err := collect(checkText("display_name", n.DisplayName, 1, 64)); err != nil {
		return User{}, err
	}
	r, err := s.openInvite(ctx, tx, token)
	if err != nil {
		return User{}, err
	}
	var accounts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE email = ?`, r.Email).Scan(&accounts); err != nil {
		return User{}, err
	}
	if accounts > 0 {
		return User{}, ErrAccountExists.with(map[string]any{"email": r.Email})
	}
	// The invitation is the permission; the new account is the actor.
	op := &writeOp{s: s, ctx: ctx, tx: tx, actor: System}
	u, err := s.insertUser(op, NewUser{Username: n.Username, DisplayName: n.DisplayName, Email: r.Email, Status: StatusActive, Locale: n.Locale})
	if err != nil {
		return User{}, err
	}
	op.actor = Actor{UserID: u.ID, Origin: OriginWeb}
	res, err := tx.ExecContext(ctx, `UPDATE invites SET accepted_at = ?, accepted_user_id = ? WHERE id = ? AND accepted_at IS NULL AND revoked_at IS NULL`,
		s.millis(), u.ID, r.ID)
	if err != nil {
		return User{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return User{}, err
	} else if n != 1 {
		return User{}, ErrInviteExpired
	}
	if err := op.record(ActionInviteAccept, inviteObject(r.ID, r.Email), map[string]any{"username": u.Username}); err != nil {
		return User{}, err
	}
	for _, p := range r.Products {
		if _, err := s.setRole(op, p, u.ID, RoleMember); err != nil {
			return User{}, err
		}
	}
	return getUser(ctx, tx, u.ID)
}
