// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// AccessToken is an access token as its owner sees it: never the token
// itself, which only exists in the response that issued it.
type AccessToken struct {
	ID         string
	Name       string
	Prefix     string // the first characters, to recognise it by
	Scope      access.TokenScope
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
}

// NewAccessToken is what an access token is issued with.
type NewAccessToken struct {
	Name          string
	Scope         access.TokenScope
	ExpiresInDays int
}

// The limits of NewAccessToken (the contract's CreateAccessTokenRequest).
const (
	maxTokenName = 64
	maxTokenDays = 366
)

func tokenObject(id, name string) audit.Object {
	return audit.Object{Kind: "access_token", ID: id, Label: name}
}

// CreateAccessToken issues an access token to the caller and returns it in
// plain, the only time it is ever returned: the database keeps its digest
// and its first characters. Only from a browser session, and only for an
// activated account.
func (s *Service) CreateAccessToken(ctx context.Context, c Caller, n NewAccessToken) (string, AccessToken, error) {
	if !c.browser() {
		return "", AccessToken{}, ErrDenied
	}
	u, err := s.liveUser(ctx, c.Actor.UserID)
	if err != nil {
		return "", AccessToken{}, err
	}
	if u.Status != access.StatusActive {
		return "", AccessToken{}, ErrDenied
	}
	n.Name = strings.TrimSpace(n.Name)
	var fields []access.FieldError
	switch l := utf8.RuneCountInString(n.Name); {
	case !utf8.ValidString(n.Name):
		fields = append(fields, access.FieldError{Field: "name", Code: "invalid"})
	case l == 0:
		fields = append(fields, access.FieldError{Field: "name", Code: "required"})
	case l > maxTokenName:
		fields = append(fields, access.FieldError{Field: "name", Code: "too_long"})
	}
	if n.Scope != access.TokenRead && n.Scope != access.TokenPublish {
		fields = append(fields, access.FieldError{Field: "scope", Code: "enum"})
	}
	if n.ExpiresInDays < 1 || n.ExpiresInDays > maxTokenDays {
		fields = append(fields, access.FieldError{Field: "expires_in_days", Code: "range"})
	}
	if len(fields) > 0 {
		return "", AccessToken{}, invalid(fields...)
	}

	plain := NewToken(AccessTokenPrefix)
	now := dbx.Time(s.millis())
	t := AccessToken{
		ID: dbx.NewID(), Name: n.Name, Prefix: tokenDisplay(plain), Scope: n.Scope,
		CreatedAt: now, ExpiresAt: now.Add(time.Duration(n.ExpiresInDays) * 24 * time.Hour),
	}
	err = s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO access_tokens (id, user_id, name, token_digest, token_prefix, scope, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, u.ID, t.Name, TokenDigest(plain), t.Prefix, string(t.Scope), dbx.Millis(t.CreatedAt), dbx.Millis(t.ExpiresAt)); err != nil {
			return err
		}
		_, err := s.audit.Append(ctx, tx, audit.Entry{
			Actor: auditActor(c.Actor), Action: ActionAccessTokenIssue, Object: tokenObject(t.ID, t.Name),
			Change: map[string]any{"scope": string(t.Scope), "expires_at": t.ExpiresAt.Format(time.RFC3339), "prefix": t.Prefix},
		})
		return err
	})
	if err != nil {
		return "", AccessToken{}, err
	}
	return plain, t, nil
}

// AccessTokens returns the caller's access tokens that are not revoked,
// newest first; expired ones are among them until revoked.
func (s *Service) AccessTokens(ctx context.Context, c Caller) ([]AccessToken, error) {
	if c.Actor.UserID == "" || c.Actor.TokenID != "" {
		return nil, ErrDenied
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, token_prefix, scope, created_at, expires_at, last_used_at
		FROM access_tokens WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at DESC, id DESC`, c.Actor.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccessToken{}
	for rows.Next() {
		var t AccessToken
		var created int64
		var expires, used sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.Scope, &created, &expires, &used); err != nil {
			return nil, err
		}
		t.CreatedAt = dbx.Time(created)
		// Every token this version issues expires; an old row without an
		// expiry shows as expiring when it was made, which keeps it from
		// looking valid.
		t.ExpiresAt = t.CreatedAt
		if expires.Valid {
			t.ExpiresAt = dbx.Time(expires.Int64)
		}
		if used.Valid {
			at := dbx.Time(used.Int64)
			t.LastUsedAt = &at
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAccessToken revokes one of the caller's access tokens: programs
// using it are refused from their next request. Revoking a revoked token
// again is not an error; someone else's is ErrNotFound.
func (s *Service) RevokeAccessToken(ctx context.Context, c Caller, id string) error {
	if c.Actor.UserID == "" || c.Actor.TokenID != "" {
		return ErrDenied
	}
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		var name string
		var revoked sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT name, revoked_at FROM access_tokens WHERE id = ? AND user_id = ?`, id, c.Actor.UserID).Scan(&name, &revoked)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil || revoked.Valid {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE access_tokens SET revoked_at = ? WHERE id = ?`, s.millis(), id); err != nil {
			return err
		}
		_, err = s.audit.Append(ctx, tx, audit.Entry{Actor: auditActor(c.Actor), Action: ActionAccessTokenDrop, Object: tokenObject(id, name)})
		return err
	})
}

// AccessToken returns the caller an access token belongs to, or
// ErrUnauthenticated when it is malformed, unknown, revoked, expired, or its
// owner's account is disabled. The caller's Actor carries the token's scope:
// access intersects it with what the owner may do at that moment.
func (s *Service) AccessToken(ctx context.Context, token string) (Caller, error) {
	if !TokenWellFormed(AccessTokenPrefix, token) {
		return Caller{}, ErrUnauthenticated
	}
	var (
		id, userID, scope string
		expires, revoked  sql.NullInt64
		used              sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, scope, expires_at, revoked_at, last_used_at FROM access_tokens WHERE token_digest = ?`,
		TokenDigest(token)).Scan(&id, &userID, &scope, &expires, &revoked, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return Caller{}, ErrUnauthenticated
	}
	if err != nil {
		return Caller{}, err
	}
	now := s.now()
	if revoked.Valid || !expires.Valid || !now.Before(dbx.Time(expires.Int64)) {
		return Caller{}, ErrUnauthenticated
	}
	if _, err := s.liveUser(ctx, userID); err != nil {
		return Caller{}, err
	}
	if !used.Valid || now.Sub(dbx.Time(used.Int64)) >= touchEvery {
		if _, err := s.db.ExecContext(ctx, `UPDATE access_tokens SET last_used_at = ? WHERE id = ?`, dbx.Millis(now), id); err != nil {
			return Caller{}, err
		}
	}
	return Caller{Actor: access.Actor{UserID: userID, Origin: access.OriginCLI, TokenID: id, Scope: access.TokenScope(scope)}}, nil
}

// Device returns the caller a device token belongs to, through DeviceTokens;
// without them (before the device code flow exists) every device token is
// ErrUnauthenticated.
func (s *Service) Device(ctx context.Context, token string) (Caller, error) {
	if s.devices == nil || !TokenWellFormed(DeviceTokenPrefix, token) {
		return Caller{}, ErrUnauthenticated
	}
	userID, deviceID, err := s.devices.VerifyDeviceToken(ctx, token)
	if err != nil {
		return Caller{}, err
	}
	if userID == "" || deviceID == "" {
		return Caller{}, ErrUnauthenticated
	}
	if _, err := s.liveUser(ctx, userID); err != nil {
		return Caller{}, err
	}
	return Caller{Actor: access.Actor{UserID: userID, Origin: access.OriginCLI, DeviceID: deviceID}}, nil
}
