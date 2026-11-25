// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// SignInInfo is how an account signs in, for the users page.
type SignInInfo struct {
	Methods     []string   // MethodLocal first, then identity provider ids in order
	LastLoginAt *time.Time // nil when it never signed in
}

// SignIns returns, by user id, how the accounts sign in. Accounts that have
// neither a password nor an identity are left out.
func (s *Service) SignIns(ctx context.Context, userIDs []string) (map[string]SignInInfo, error) {
	out := map[string]SignInInfo{}
	if len(userIDs) == 0 {
		return out, nil
	}
	in := strings.TrimSuffix(strings.Repeat("?, ", len(userIDs)), ", ")
	args := make([]any, len(userIDs))
	for i, id := range userIDs {
		args[i] = id
	}
	later := func(info *SignInInfo, at sql.NullInt64) {
		if !at.Valid {
			return
		}
		t := dbx.Time(at.Int64)
		if info.LastLoginAt == nil || t.After(*info.LastLoginAt) {
			info.LastLoginAt = &t
		}
	}

	rows, err := s.db.QueryContext(ctx, `SELECT user_id, last_login_at FROM local_credentials WHERE user_id IN (`+in+`)`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var at sql.NullInt64
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			return nil, err
		}
		info := out[id]
		info.Methods = append(info.Methods, MethodLocal)
		later(&info, at)
		out[id] = info
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT user_id, provider, last_login_at FROM identities WHERE user_id IN (`+in+`) ORDER BY provider`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, provider string
		var at sql.NullInt64
		if err := rows.Scan(&id, &provider, &at); err != nil {
			return nil, err
		}
		info := out[id]
		if !slices.Contains(info.Methods, provider) {
			info.Methods = append(info.Methods, provider)
		}
		later(&info, at)
		out[id] = info
	}
	return out, rows.Err()
}
