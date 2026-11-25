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

// lockFor is how long an account stays locked after its failures-th wrong
// password in a row: nothing for the first few (typing mistakes), then a
// delay that doubles with every failure, then a lock of a quarter of an
// hour. A right password resets the count.
//
//	failures  1–3  4   5   6   7    8    9    10+
//	locked    –    1s  2s  4s  8s   16s  32s  15m
func lockFor(failures int) time.Duration {
	const free, lockAt = 3, 10
	switch {
	case failures <= free:
		return 0
	case failures >= lockAt:
		return 15 * time.Minute
	}
	return time.Second << (failures - free - 1)
}

// credential is a row of local_credentials.
type credential struct {
	hash        string
	failures    int
	lockedUntil sql.NullInt64
}

func readCredential(ctx context.Context, q dbx.Querier, userID string) (credential, bool, error) {
	var c credential
	err := q.QueryRowContext(ctx, `SELECT password_hash, failed_attempts, locked_until FROM local_credentials WHERE user_id = ?`, userID).
		Scan(&c.hash, &c.failures, &c.lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

// lockedFor returns how much longer the credential stays locked; zero or
// less when it is not locked.
func (c credential) lockedFor(now time.Time) time.Duration {
	if !c.lockedUntil.Valid {
		return 0
	}
	return dbx.Time(c.lockedUntil.Int64).Sub(now)
}

// putCredential sets an account's password hash and clears its failures.
func (s *Service) putCredential(ctx context.Context, tx *dbx.Tx, userID, hash string, loggedIn bool) error {
	now := s.millis()
	var last any
	if loggedIn {
		last = now
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO local_credentials (user_id, password_hash, failed_attempts, locked_until, updated_at, last_login_at)
		VALUES (?, ?, 0, NULL, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET password_hash = excluded.password_hash, failed_attempts = 0, locked_until = NULL,
			updated_at = excluded.updated_at, last_login_at = COALESCE(excluded.last_login_at, local_credentials.last_login_at)`,
		userID, hash, now, last)
	return err
}

// recordFailure counts a wrong password against userID in tx, locks the
// account for lockFor of the new count, and returns the count.
func (s *Service) recordFailure(ctx context.Context, tx *dbx.Tx, userID string) (int, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `UPDATE local_credentials SET failed_attempts = failed_attempts + 1 WHERE user_id = ? RETURNING failed_attempts`,
		userID).Scan(&n); err != nil {
		return 0, err
	}
	var until any
	if d := lockFor(n); d > 0 {
		until = dbx.Millis(s.now().Add(d))
	}
	_, err := tx.ExecContext(ctx, `UPDATE local_credentials SET locked_until = ? WHERE user_id = ?`, until, userID)
	return n, err
}

// userByName finds the account with exactly this account name (lower
// case); false when there is none.
//
// TODO: access has no lookup by account name yet; ListUsers' search plus an
// exact comparison does the same, one LIKE over the users table per sign-in.
func (s *Service) userByName(ctx context.Context, name string) (access.User, bool, error) {
	us, err := s.access.ListUsers(ctx, access.System, access.UserFilter{Query: name})
	if err != nil {
		return access.User{}, false, err
	}
	for _, u := range us {
		if u.Username == name {
			return u, true, nil
		}
	}
	return access.User{}, false, nil
}

// failedLogin records a refused sign-in in a transaction of its own.
func (s *Service) failedLogin(ctx context.Context, object audit.Object, reason string, change map[string]any) error {
	_, err := s.audit.Record(ctx, audit.Entry{
		Category: audit.CategoryLogin, Method: MethodLocal, Actor: audit.Actor{Origin: audit.OriginWeb},
		Action: ActionLogin, Object: object, Change: change, Result: audit.ResultDenied, Reason: reason,
	})
	return err
}

// The reasons of refused sign-ins in the audit log.
const (
	reasonUnknownAccount     = "unknown_account"
	reasonNoPassword         = "no_password"
	reasonLocked             = "locked"
	reasonInvalidCredentials = "invalid_credentials"
	reasonDisabled           = "disabled"
)

// Login signs in with an account name and a password and starts a session.
//
// A wrong account name and a wrong password get the same answer
// (ErrInvalidCredentials) after the same amount of work. A locked account
// gets ErrLocked without its password being checked; a disabled one gets
// ErrDisabled, but only with the right password. Every attempt that reaches
// this method is in the audit log; the password never is.
func (s *Service) Login(ctx context.Context, username, password string) (SignedIn, error) {
	name := strings.ToLower(strings.TrimSpace(username))
	label := name
	if utf8.RuneCountInString(label) > 64 || !utf8.ValidString(label) {
		label = "(not an account name)"
	}
	nobody := audit.Object{Kind: "user", Label: label}
	if label != name || name == "" {
		s.hasher.burn(ctx, "")
		return SignedIn{}, refused(ErrInvalidCredentials, s.failedLogin(ctx, nobody, reasonUnknownAccount, nil))
	}
	if len(password) > 4*MaxPasswordLength {
		// Longer than any password can be: wrong, without hashing it.
		s.hasher.burn(ctx, "")
		return SignedIn{}, refused(ErrInvalidCredentials, s.failedLogin(ctx, nobody, reasonInvalidCredentials, nil))
	}
	u, found, err := s.userByName(ctx, name)
	if err != nil {
		return SignedIn{}, err
	}
	if !found {
		s.hasher.burn(ctx, password)
		return SignedIn{}, refused(ErrInvalidCredentials, s.failedLogin(ctx, nobody, reasonUnknownAccount, nil))
	}
	object := userObject(u.ID, u.Username)
	cred, ok, err := readCredential(ctx, s.db, u.ID)
	if err != nil {
		return SignedIn{}, err
	}
	if !ok {
		s.hasher.burn(ctx, password)
		return SignedIn{}, refused(ErrInvalidCredentials, s.failedLogin(ctx, object, reasonNoPassword, nil))
	}
	if wait := cred.lockedFor(s.now()); wait > 0 {
		return SignedIn{}, refused(locked(wait), s.failedLogin(ctx, object, reasonLocked, nil))
	}
	match, rehash, err := s.hasher.verify(ctx, cred.hash, password)
	if err != nil {
		return SignedIn{}, err
	}
	if !match {
		var failures int
		err := s.db.InTx(ctx, func(tx *dbx.Tx) error {
			var err error
			if failures, err = s.recordFailure(ctx, tx, u.ID); err != nil {
				return err
			}
			_, err = s.audit.Append(ctx, tx, audit.Entry{
				Category: audit.CategoryLogin, Method: MethodLocal, Actor: audit.Actor{Origin: audit.OriginWeb},
				Action: ActionLogin, Object: object, Change: map[string]any{"failed_attempts": failures},
				Result: audit.ResultDenied, Reason: reasonInvalidCredentials,
			})
			return err
		})
		return SignedIn{}, refused(ErrInvalidCredentials, err)
	}
	if u.Status != access.StatusActive && u.Status != access.StatusPending {
		return SignedIn{}, refused(ErrDisabled, s.failedLogin(ctx, object, reasonDisabled, nil))
	}

	newHash := cred.hash
	if rehash {
		if newHash, err = s.hasher.hash(ctx, password); err != nil {
			return SignedIn{}, err
		}
	}
	var in SignedIn
	err = s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE local_credentials SET password_hash = ?, failed_attempts = 0, locked_until = NULL, last_login_at = ? WHERE user_id = ?`,
			newHash, s.millis(), u.ID); err != nil {
			return err
		}
		var err error
		if in, err = s.startSession(ctx, tx, u.ID); err != nil {
			return err
		}
		_, err = s.audit.Append(ctx, tx, loginEntry(u, nil))
		return err
	})
	in.User = u
	return in, err
}

// FirstAdmin is the setup wizard's first administrator.
type FirstAdmin struct {
	Username    string
	DisplayName string
	Email       string
	Password    string
}

// CreateFirstAdmin creates a local account that is a platform administrator,
// with its password, inside tx: the setup wizard's transaction, which checks
// the setup token and marks the step done (settings.CreateFirstAdmin).
// StartSession signs it in after the commit.
func (s *Service) CreateFirstAdmin(ctx context.Context, tx *dbx.Tx, a FirstAdmin) (access.User, error) {
	if err := checkPassword("password", a.Password, strings.ToLower(a.Username)); err != nil {
		return access.User{}, err
	}
	hash, err := s.hasher.hash(ctx, a.Password)
	if err != nil {
		return access.User{}, err
	}
	u, err := s.access.CreateUser(ctx, tx, access.System, access.NewUser{
		Username: a.Username, DisplayName: a.DisplayName, Email: a.Email, Status: access.StatusActive, Admin: true,
	})
	if err != nil {
		return access.User{}, err
	}
	return u, s.putCredential(ctx, tx, u.ID, hash, false)
}

// AcceptInvite creates the account an invitation link is for, with a
// password, and signs it in: access.AcceptInvite, the password and the
// session in one transaction, so that a failure leaves the link usable.
func (s *Service) AcceptInvite(ctx context.Context, inviteToken string, n access.NewAccount, password string) (SignedIn, error) {
	if err := checkPassword("password", password, strings.ToLower(n.Username)); err != nil {
		return SignedIn{}, err
	}
	// Hash before the transaction, which holds the write lock on SQLite.
	hash, err := s.hasher.hash(ctx, password)
	if err != nil {
		return SignedIn{}, err
	}
	var in SignedIn
	err = s.db.InTx(ctx, func(tx *dbx.Tx) error {
		u, err := s.access.AcceptInvite(ctx, tx, inviteToken, n)
		if err != nil {
			return err
		}
		if err := s.putCredential(ctx, tx, u.ID, hash, true); err != nil {
			return err
		}
		if in, err = s.startSession(ctx, tx, u.ID); err != nil {
			return err
		}
		in.User = u
		_, err = s.audit.Append(ctx, tx, loginEntry(u, map[string]any{"via": "invite"}))
		return err
	})
	if err != nil {
		return SignedIn{}, err
	}
	return in, nil
}

// ChangePassword replaces the caller's password. The current one must be
// given, and a wrong one counts towards the lock like a failed sign-in.
// Every other session of the account ends; the caller's stays.
func (s *Service) ChangePassword(ctx context.Context, c Caller, current, next string) error {
	if !c.browser() {
		return ErrDenied
	}
	u, err := s.liveUser(ctx, c.Actor.UserID)
	if err != nil {
		return err
	}
	object := userObject(u.ID, u.Username)
	actor := auditActor(c.Actor)
	cred, ok, err := readCredential(ctx, s.db, u.ID)
	switch {
	case err != nil:
		return err
	case !ok:
		return ErrNoPassword
	}
	if wait := cred.lockedFor(s.now()); wait > 0 {
		_, err := s.audit.Record(ctx, audit.Entry{Actor: actor, Action: ActionPasswordChange, Object: object, Result: audit.ResultDenied, Reason: reasonLocked})
		return refused(locked(wait), err)
	}
	if err := checkPassword("new_password", next, u.Username); err != nil {
		return err
	}
	if len(current) > 4*MaxPasswordLength {
		current = ""
	}
	match, _, err := s.hasher.verify(ctx, cred.hash, current)
	if err != nil {
		return err
	}
	if !match {
		err := s.db.InTx(ctx, func(tx *dbx.Tx) error {
			failures, err := s.recordFailure(ctx, tx, u.ID)
			if err != nil {
				return err
			}
			_, err = s.audit.Append(ctx, tx, audit.Entry{
				Actor: actor, Action: ActionPasswordChange, Object: object, Change: map[string]any{"failed_attempts": failures},
				Result: audit.ResultDenied, Reason: reasonInvalidCredentials,
			})
			return err
		})
		return refused(invalid(access.FieldError{Field: "current_password", Code: "mismatch"}), err)
	}
	hash, err := s.hasher.hash(ctx, next)
	if err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if err := s.putCredential(ctx, tx, u.ID, hash, false); err != nil {
			return err
		}
		ended, err := s.endSessions(ctx, tx, u.ID, c.SessionID)
		if err != nil {
			return err
		}
		_, err = s.audit.Append(ctx, tx, audit.Entry{
			Actor: actor, Action: ActionPasswordChange, Object: object,
			Change: map[string]any{"password": "replaced", "sessions_ended": ended},
		})
		return err
	})
}

// refused returns the answer to a refused attempt, unless recording it in the
// audit log failed: an attempt that cannot be recorded is a fault, not an
// answer.
func refused(answer, auditErr error) error {
	if auditErr != nil {
		return auditErr
	}
	return answer
}
