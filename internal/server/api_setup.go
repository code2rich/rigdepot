// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"net/http"

	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// setupAPI implements the operations tagged setup: the first-run wizard.
// Once the wizard is complete every one of them answers 404 setup.closed.
type setupAPI struct {
	settings *settings.Service
	hooks    *hooks
}

// firstAdminCreator makes the wizard's first administrator. The account and
// the session belong to access and authn (M1-12, M1-20); settings only
// checks the token and the wizard's state around it.
type firstAdminCreator interface {
	// CreateFirstAdmin validates the request (account name, password
	// strength) and creates a local account that is a platform
	// administrator, inside tx: the same transaction that marks the step
	// done and writes the audit entry. A *httpapi.ProblemError is answered
	// as is.
	CreateFirstAdmin(ctx context.Context, tx *dbx.Tx, req httpapi.SetupAdminRequest) (settings.NewAdmin, error)
	// SignIn starts a session for the new administrator, after the commit,
	// and returns the Set-Cookie header value and the account as /me shows
	// it.
	SignIn(ctx context.Context, userID string) (setCookie string, me httpapi.Me, err error)
}

var errSetupClosed = httpapi.NewProblem(http.StatusNotFound, "setup.closed", "Setup is complete.")

// open answers setup.closed once the wizard is complete.
func (s *setupAPI) open(ctx context.Context) (settings.SetupStatus, error) {
	st, err := s.settings.Setup(ctx)
	if err != nil {
		return st, err
	}
	if st.Complete {
		return st, errSetupClosed
	}
	return st, nil
}

func (s *setupAPI) GetSetup(ctx context.Context, _ httpapi.GetSetupRequestObject) (httpapi.GetSetupResponseObject, error) {
	st, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	return httpapi.GetSetup200JSONResponse{AdminCreated: st.AdminCreated, SetupTokenRequired: true}, nil
}

func (s *setupAPI) CreateSetupAdmin(ctx context.Context, req httpapi.CreateSetupAdminRequestObject) (httpapi.CreateSetupAdminResponseObject, error) {
	if _, err := s.open(ctx); err != nil {
		return nil, err
	}
	if s.hooks.firstAdmin == nil {
		return nil, httpapi.ErrNotImplemented
	}
	if req.Body == nil {
		return nil, httpapi.NewProblem(http.StatusBadRequest, httpapi.CodeRequestInvalid, "The request has no body.")
	}
	admin, err := s.settings.CreateFirstAdmin(ctx, req.Params.XSetupToken, func(tx *dbx.Tx) (settings.NewAdmin, error) {
		return s.hooks.firstAdmin.CreateFirstAdmin(ctx, tx, *req.Body)
	})
	if err != nil {
		return nil, settingsProblem(err)
	}
	cookie, me, err := s.hooks.firstAdmin.SignIn(ctx, admin.UserID)
	if err != nil {
		return nil, err
	}
	return httpapi.CreateSetupAdmin201JSONResponse{Body: me, Headers: httpapi.CreateSetupAdmin201ResponseHeaders{SetCookie: &cookie}}, nil
}

func (s *setupAPI) CompleteSetup(ctx context.Context, _ httpapi.CompleteSetupRequestObject) (httpapi.CompleteSetupResponseObject, error) {
	if _, err := s.open(ctx); err != nil {
		return nil, err
	}
	actor, ok := callerFrom(ctx)
	if !ok {
		return nil, errSignInRequired
	}
	if err := s.settings.CompleteSetup(ctx, actor); err != nil {
		return nil, settingsProblem(err)
	}
	return httpapi.CompleteSetup204Response{}, nil
}

// settingsAuthorizer gives the settings package the access package's
// decisions, through hooks.authorize as it is when asked.
func (h *hooks) settingsAuthorizer() settings.Authorizer {
	return func(ctx context.Context, actor audit.Actor, action string) (bool, error) {
		if h.authorize == nil {
			return false, nil
		}
		return h.authorize(ctx, actor, action, audit.Object{})
	}
}
