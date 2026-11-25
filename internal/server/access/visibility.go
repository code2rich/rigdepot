// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access

import (
	"context"
	"database/sql"
	"errors"
	"slices"
)

// Visibility is which scopes a person is in, for listing and delivering
// resources (docs/subsystems/access-roles.md#可见性): the whole organisation
// once their account is active, the products they are a member or owner of,
// and their own personal scope. It is read fresh for every request; taking
// someone out of a product takes effect with their next one.
//
// It answers "whose is this for", which is what delivery needs. What a
// platform administrator may look at in the console without being in the
// scope (every product, the metadata of personal resources) is a question
// for Can with ActionResourceRead.
type Visibility struct {
	UserID   string
	Active   bool     // false: pending or disabled, sees nothing
	Admin    bool     // platform administrator
	Products []string // ids of the products they are in, sorted
}

// VisibilityOf reads the scopes the actor is in.
func (s *Service) VisibilityOf(ctx context.Context, a Actor) (Visibility, error) {
	v := Visibility{UserID: a.UserID, Products: []string{}}
	if a.UserID == "" {
		return v, nil
	}
	sub, err := loadSubject(ctx, s.db, a.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if sub.status != StatusActive {
		return v, nil
	}
	v.Active, v.Admin = true, sub.admin
	for id := range sub.roles {
		v.Products = append(v.Products, id)
	}
	slices.Sort(v.Products)
	return v, nil
}

// Includes reports whether something in scope t is for this person: they
// would see it and get it on their computers.
func (v Visibility) Includes(t Target) bool {
	if !v.Active {
		return false
	}
	switch t.Scope {
	case InOrg:
		return true
	case InProduct:
		_, found := slices.BinarySearch(v.Products, t.ProductID)
		return t.ProductID != "" && found
	case InPersonal:
		return t.UserID != "" && t.UserID == v.UserID
	}
	return false
}
