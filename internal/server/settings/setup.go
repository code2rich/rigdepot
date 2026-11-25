// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package settings

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// SetupStatus is where the setup wizard stands.
type SetupStatus struct {
	AdminCreated bool // the first administrator exists; the rest runs as them
	Complete     bool // the wizard is closed for good
}

// The wizard's actions, also the audit's.
const (
	ActionSetupAdmin    = "setup.admin_create"
	ActionSetupComplete = "setup.complete"
)

var setupObject = audit.Object{Kind: "setup", ID: "wizard", Label: "Setup wizard"}

// Setup returns the wizard's state.
func (s *Service) Setup(ctx context.Context) (SetupStatus, error) {
	return setupStatus(ctx, s.db)
}

func setupStatus(ctx context.Context, q dbx.Querier) (SetupStatus, error) {
	var st SetupStatus
	var at int64
	found, err := getJSON(ctx, q, keyAdminCreated, &at)
	if err != nil {
		return st, err
	}
	st.AdminCreated = found
	found, err = getJSON(ctx, q, keySetupDone, &at)
	st.Complete = found
	return st, err
}

// SetupToken returns the one-time setup token the wizard accepts and whether
// the server made it (and so should print it). A token from the
// configuration is returned as is and never stored; otherwise the first call
// generates one and keeps it sealed, so that every instance and every
// restart accept the same one until the wizard is complete. It returns ""
// once the wizard is complete.
func (s *Service) SetupToken(ctx context.Context) (token string, generated bool, err error) {
	st, err := s.Setup(ctx)
	if err != nil || st.Complete {
		return "", false, err
	}
	if s.opts.SetupToken != "" {
		return s.opts.SetupToken, false, nil
	}
	err = s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if err := tx.Lock(ctx, lockKey); err != nil {
			return err
		}
		stored, found, err := s.getSealed(ctx, tx, keySetupToken)
		if err != nil || found {
			token = string(stored)
			return err
		}
		token = newSetupToken()
		return s.putSealed(ctx, tx, keySetupToken, []byte(token), audit.System)
	})
	return token, true, err
}

// newSetupToken is 160 random bits, in groups of four characters that are
// easy to copy from a log.
func newSetupToken() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic("settings: no randomness: " + err.Error())
	}
	raw := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	var groups []string
	for i := 0; i < len(raw); i += 4 {
		groups = append(groups, raw[i:min(i+4, len(raw))])
	}
	return strings.Join(groups, "-")
}

// Wrong setup tokens are limited: a few per window, then refused until the
// window has passed. The token has 160 bits, so this is about noise in the
// audit log more than guessing.
const (
	tokenFailures = 10
	tokenWindow   = 15 * time.Minute
)

func (s *Service) limited() bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	cutoff := s.now().Add(-tokenWindow)
	kept := s.failures[:0]
	for _, t := range s.failures {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.failures = kept
	return len(kept) >= tokenFailures
}

func (s *Service) failed() {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	s.failures = append(s.failures, s.now())
}

// NewAdmin is the account CreateFirstAdmin's callback created.
type NewAdmin struct {
	UserID   string
	Username string
}

// CreateFirstAdmin creates the first platform administrator: it checks that
// the wizard is open, that token is the setup token and that no
// administrator was created through the wizard yet, then calls create to make
// the account inside the same transaction that marks the step done and
// writes the audit entry. The account itself belongs to the access and
// authn packages, which provide create.
func (s *Service) CreateFirstAdmin(ctx context.Context, token string, create func(tx *dbx.Tx) (NewAdmin, error)) (NewAdmin, error) {
	if s.limited() {
		return NewAdmin{}, ErrSetupRateLimited
	}
	want, _, err := s.SetupToken(ctx)
	if err != nil {
		return NewAdmin{}, err
	}
	if want == "" {
		return NewAdmin{}, ErrSetupClosed
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(want)) != 1 {
		s.failed()
		if _, err := s.log.Record(ctx, audit.Entry{Actor: audit.Actor{Origin: audit.OriginWeb}, Action: ActionSetupAdmin,
			Object: setupObject, Result: audit.ResultDenied, Reason: "wrong setup token"}); err != nil {
			return NewAdmin{}, err
		}
		return NewAdmin{}, ErrSetupToken
	}
	var admin NewAdmin
	err = s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if err := tx.Lock(ctx, lockKey); err != nil {
			return err
		}
		st, err := setupStatus(ctx, tx)
		switch {
		case err != nil:
			return err
		case st.Complete:
			return ErrSetupClosed
		case st.AdminCreated:
			return ErrAdminExists
		}
		if admin, err = create(tx); err != nil {
			return err
		}
		actor := audit.Actor{UserID: admin.UserID, Origin: audit.OriginWeb}
		if err := putJSON(ctx, tx, keyAdminCreated, s.now().UnixMilli(), actor, s.now()); err != nil {
			return err
		}
		_, err = s.log.Append(ctx, tx, audit.Entry{Actor: actor, Action: ActionSetupAdmin,
			Object: audit.Object{Kind: "user", ID: admin.UserID, Label: admin.Username}})
		return err
	})
	if err != nil {
		return NewAdmin{}, err
	}
	return admin, nil
}

// CompleteSetup closes the wizard for good: the setup token stops working
// and the setup operations answer "closed" from now on. Platform
// administrators only.
func (s *Service) CompleteSetup(ctx context.Context, actor audit.Actor) error {
	if st, err := s.Setup(ctx); err != nil {
		return err
	} else if st.Complete {
		return ErrSetupClosed
	}
	if err := s.authorize(ctx, actor, ActionSetupComplete, setupObject); err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if err := tx.Lock(ctx, lockKey); err != nil {
			return err
		}
		st, err := setupStatus(ctx, tx)
		if err != nil {
			return err
		}
		if st.Complete {
			return ErrSetupClosed
		}
		if err := putJSON(ctx, tx, keySetupDone, s.now().UnixMilli(), actor, s.now()); err != nil {
			return err
		}
		if err := remove(ctx, tx, keySetupToken); err != nil {
			return err
		}
		_, err = s.log.Append(ctx, tx, audit.Entry{Actor: actor, Action: ActionSetupComplete, Object: setupObject,
			Change: map[string]any{"admin_created": st.AdminCreated}})
		return err
	})
}
