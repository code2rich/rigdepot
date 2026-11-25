// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// ScopeKind is the kind of scope an object is in. The values are those of
// the resources table's scope_kind column.
type ScopeKind string

// The three scopes of docs/subsystems/access-roles.md#可见性.
const (
	InOrg      ScopeKind = "org"     // everyone who is activated
	InProduct  ScopeKind = "product" // the members and owners of one product
	InPersonal ScopeKind = "me"      // only its author
)

// Target is what an action is done to, described by what the permission
// rules look at. The zero Target is the platform itself (settings, audit…).
type Target struct {
	Scope       ScopeKind // empty for objects outside the three scopes (accounts, the platform)
	ProductID   string    // the product, when Scope is InProduct or the target is a product
	UserID      string    // the person the target belongs to: a personal resource's author, an account
	Maintainers []string  // user IDs of the resource's maintainers
}

// TargetPlatform is the platform as a whole.
func TargetPlatform() Target { return Target{} }

// TargetOrg is something visible to the whole organisation.
func TargetOrg() Target { return Target{Scope: InOrg} }

// TargetProduct is a product, or something in its scope.
func TargetProduct(productID string) Target { return Target{Scope: InProduct, ProductID: productID} }

// TargetPersonal is something only userID has.
func TargetPersonal(userID string) Target { return Target{Scope: InPersonal, UserID: userID} }

// TargetAccount is a person's account, or something of theirs outside the
// three scopes (a join request, their profile).
func TargetAccount(userID string) Target { return Target{UserID: userID} }

// TargetResource describes a resource for a decision: its scope (product ID
// for InProduct, author for InPersonal) and its maintainers.
func TargetResource(scope ScopeKind, productID, authorID string, maintainers []string) Target {
	return Target{Scope: scope, ProductID: productID, UserID: authorID, Maintainers: maintainers}
}

// Decision is the answer to "may this actor do this". Err is set when the
// answer could not be found out (the database failed); Allowed is then
// false, because an unknown answer is a no.
type Decision struct {
	Allowed bool
	Reason  string // why not, for the audit log: one of the Reason constants
	Err     error
}

// Why an action was denied.
const (
	ReasonNotAllowed  = "not_allowed"    // no rule grants it
	ReasonPending     = "pending"        // the account is not activated yet
	ReasonDisabled    = "disabled"       // the account is disabled
	ReasonUnknownUser = "unknown_user"   // the actor's account does not exist
	ReasonTokenScope  = "token_scope"    // the access token's scope does not cover it
	ReasonUnknown     = "unknown_action" // the action has no rule
	ReasonError       = "error"          // the decision could not be made
)

// Status of an account.
type Status string

// The statuses of the users table.
const (
	StatusPending  Status = "pending"
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

// Role is someone's role in a product.
type Role string

// The roles of the memberships table.
const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
)

// subject is what decisions know about the acting person, read from the
// database for every decision.
type subject struct {
	id     string
	status Status
	admin  bool
	roles  map[string]Role // product ID -> role
}

func loadSubject(ctx context.Context, q dbx.Querier, userID string) (subject, error) {
	s := subject{id: userID, roles: map[string]Role{}}
	err := q.QueryRowContext(ctx, `SELECT status, is_admin FROM users WHERE id = ?`, userID).Scan(&s.status, &s.admin)
	if err != nil {
		return s, err
	}
	rows, err := q.QueryContext(ctx, `SELECT product_id, role FROM memberships WHERE user_id = ?`, userID)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var r Role
		if err := rows.Scan(&p, &r); err != nil {
			return s, err
		}
		s.roles[p] = r
	}
	return s, rows.Err()
}

// Can decides whether a may do action to t, from the state of the database
// at this moment. A database error is a denial with Err set.
func (s *Service) Can(ctx context.Context, a Actor, action Action, t Target) Decision {
	return s.CanIn(ctx, s.db, a, action, t)
}

// CanIn is Can reading through q, such as the transaction the action is
// about to run in.
func (s *Service) CanIn(ctx context.Context, q dbx.Querier, a Actor, action Action, t Target) Decision {
	r, ok := rules[action]
	if !ok {
		return Decision{Reason: ReasonUnknown}
	}
	if a.IsSystem() {
		return Decision{Allowed: true}
	}
	if a.UserID == "" {
		return Decision{Reason: ReasonUnknownUser}
	}
	sub, err := loadSubject(ctx, q, a.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return Decision{Reason: ReasonUnknownUser}
	}
	if err != nil {
		return Decision{Reason: ReasonError, Err: err}
	}
	return decide(sub, a.Scope, r, t)
}

// decide applies one rule. It is the whole permission model; everything
// else only gathers its inputs.
func decide(sub subject, token TokenScope, r rule, t Target) Decision {
	switch sub.status {
	case StatusActive:
	case StatusPending:
		if !r.pending {
			return Decision{Reason: ReasonPending}
		}
	case StatusDisabled:
		return Decision{Reason: ReasonDisabled}
	default:
		// A status this version does not know is not a status that may act.
		return Decision{Reason: ReasonDisabled}
	}
	// An access token can do at most what its scope covers, and never more
	// than its owner (the grants below).
	switch token {
	case "":
	case TokenRead:
		if r.token != TokenRead {
			return Decision{Reason: ReasonTokenScope}
		}
	case TokenPublish:
		if r.token != TokenRead && r.token != TokenPublish {
			return Decision{Reason: ReasonTokenScope}
		}
	default:
		return Decision{Reason: ReasonTokenScope}
	}

	active := sub.status == StatusActive
	role, inProduct := Role(""), false
	if t.ProductID != "" {
		role, inProduct = sub.roles[t.ProductID]
	}
	othersPersonal := t.Scope == InPersonal && t.UserID != sub.id
	switch {
	case sub.admin && active && r.admin == adminAll,
		sub.admin && active && r.admin == adminShared && !othersPersonal,
		r.anyone,
		r.self && t.UserID != "" && t.UserID == sub.id,
		r.owner && inProduct && role == RoleOwner,
		r.member && inProduct,
		r.anyOwner && slices.Contains(roleValues(sub.roles), RoleOwner),
		r.maintainer && active && slices.Contains(t.Maintainers, sub.id),
		r.inScope && active && visible(sub, t):
		return Decision{Allowed: true}
	}
	return Decision{Reason: ReasonNotAllowed}
}

// visible reports whether sub is among the people a scope is for.
func visible(sub subject, t Target) bool {
	switch t.Scope {
	case InOrg:
		return true
	case InProduct:
		_, ok := sub.roles[t.ProductID]
		return t.ProductID != "" && ok
	case InPersonal:
		return t.UserID != "" && t.UserID == sub.id
	}
	return false
}

func roleValues(m map[string]Role) []Role {
	out := make([]Role, 0, len(m))
	for _, r := range m {
		out = append(out, r)
	}
	return out
}
