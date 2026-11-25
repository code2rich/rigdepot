// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// The lifetime of a browser session (docs/architecture/06-server.md#鉴权).
const (
	SessionIdle = 7 * 24 * time.Hour  // ends after this long without a request
	SessionMax  = 30 * 24 * time.Hour // ends this long after signing in, whatever happens
)

// touchEvery is how stale last_seen_at (and an access token's last_used_at)
// may get before a request writes it again: one write a minute per session
// at most, not one per request.
const touchEvery = time.Minute

// SignedIn is a new session.
type SignedIn struct {
	User      access.User
	Token     string // the cookie's value; only this response ever has it
	SessionID string
	ExpiresAt time.Time
}

// startSession adds a session for userID in tx.
func (s *Service) startSession(ctx context.Context, tx *dbx.Tx, userID string) (SignedIn, error) {
	now := s.now()
	in := SignedIn{Token: newSessionToken(), SessionID: dbx.NewID(), ExpiresAt: now.Add(SessionMax)}
	_, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, user_id, token_digest, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		in.SessionID, userID, TokenDigest(in.Token), dbx.Millis(now), dbx.Millis(now), dbx.Millis(in.ExpiresAt))
	return in, err
}

// loginEntry is the audit entry of a sign-in.
func loginEntry(u access.User, change map[string]any) audit.Entry {
	return audit.Entry{
		Category: audit.CategoryLogin, Method: MethodLocal, Actor: webActor(u.ID),
		Action: ActionLogin, Object: userObject(u.ID, u.Username), Change: change,
	}
}

// StartSession signs userID in with a new session, for the setup wizard's
// first administrator right after the account was made. The sign-in is in
// the audit log.
func (s *Service) StartSession(ctx context.Context, userID string) (SignedIn, error) {
	u, err := s.liveUser(ctx, userID)
	if err != nil {
		return SignedIn{}, err
	}
	var in SignedIn
	err = s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if in, err = s.startSession(ctx, tx, u.ID); err != nil {
			return err
		}
		_, err = s.audit.Append(ctx, tx, loginEntry(u, map[string]any{"via": "setup"}))
		return err
	})
	in.User = u
	return in, err
}

// Session returns the caller a session cookie belongs to, or
// ErrUnauthenticated when the session is unknown, signed out, idle for
// longer than SessionIdle, older than SessionMax, or its account is
// disabled.
func (s *Service) Session(ctx context.Context, token string) (Caller, error) {
	if token == "" || len(token) > 128 {
		return Caller{}, ErrUnauthenticated
	}
	var (
		id, userID        string
		lastSeen, expires int64
		revoked           sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, last_seen_at, expires_at, revoked_at FROM sessions WHERE token_digest = ?`,
		TokenDigest(token)).Scan(&id, &userID, &lastSeen, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Caller{}, ErrUnauthenticated
	}
	if err != nil {
		return Caller{}, err
	}
	now := s.now()
	if revoked.Valid || !now.Before(dbx.Time(expires)) || !now.Before(dbx.Time(lastSeen).Add(SessionIdle)) {
		return Caller{}, ErrUnauthenticated
	}
	if _, err := s.liveUser(ctx, userID); err != nil {
		return Caller{}, err
	}
	if now.Sub(dbx.Time(lastSeen)) >= touchEvery {
		if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ? AND revoked_at IS NULL`, dbx.Millis(now), id); err != nil {
			return Caller{}, err
		}
	}
	return Caller{Actor: access.Actor{UserID: userID, Origin: access.OriginWeb}, SessionID: id}, nil
}

// Logout ends the caller's session.
func (s *Service) Logout(ctx context.Context, c Caller) error {
	if c.SessionID == "" {
		return ErrDenied
	}
	u, err := s.access.GetUser(ctx, access.System, c.Actor.UserID)
	if err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
			s.millis(), c.SessionID, c.Actor.UserID); err != nil {
			return err
		}
		_, err := s.audit.Append(ctx, tx, audit.Entry{
			Category: audit.CategoryLogin, Actor: auditActor(c.Actor), Action: ActionLogout, Object: userObject(u.ID, u.Username),
		})
		return err
	})
}

// endSessions ends the account's sessions except keep (which may be "") in
// tx and returns how many it ended.
func (s *Service) endSessions(ctx context.Context, tx *dbx.Tx, userID, keep string) (int64, error) {
	res, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND id <> ? AND revoked_at IS NULL AND expires_at > ?`,
		s.millis(), userID, keep, s.millis())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SignOutEverywhere ends every session of the caller's account, the
// caller's own included, and signs out every computer.
func (s *Service) SignOutEverywhere(ctx context.Context, c Caller) error {
	if c.Actor.UserID == "" || c.Actor.TokenID != "" {
		return ErrDenied
	}
	u, err := s.access.GetUser(ctx, access.System, c.Actor.UserID)
	if err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		sessions, err := s.endSessions(ctx, tx, u.ID, "")
		if err != nil {
			return err
		}
		devices := 0
		if s.devices != nil {
			if devices, err = s.devices.SignOutDevices(ctx, tx, u.ID); err != nil {
				return err
			}
		}
		_, err = s.audit.Append(ctx, tx, audit.Entry{
			Category: audit.CategoryLogin, Actor: auditActor(c.Actor), Action: ActionSignOutAll, Object: userObject(u.ID, u.Username),
			Change: map[string]any{"sessions": sessions, "devices": devices},
		})
		return err
	})
}

// PurgeSessions deletes the sessions that can no longer be used: signed
// out, expired, or idle for longer than SessionIdle. It runs as a background
// job; a session it misses is still refused by Session.
func (s *Service) PurgeSessions(ctx context.Context) error {
	now := s.now()
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE revoked_at IS NOT NULL OR expires_at <= ? OR last_seen_at <= ?`,
		dbx.Millis(now), dbx.Millis(now.Add(-SessionIdle)))
	return err
}
