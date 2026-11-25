// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package accessapi

import (
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

func (h *Handlers) signIns(ctx context.Context, users []access.User) (map[string]SignIn, error) {
	if h.o.SignIns == nil || len(users) == 0 {
		return map[string]SignIn{}, nil
	}
	ids := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	return h.o.SignIns(ctx, ids)
}

func email(v string) *openapi_types.Email {
	if v == "" {
		return nil
	}
	e := openapi_types.Email(v)
	return &e
}

func user(u access.User, s SignIn) httpapi.User {
	verified := u.EmailVerified
	out := httpapi.User{
		Id: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: email(u.Email),
		Status: httpapi.UserStatus(u.Status), PlatformAdmin: u.Admin, Job: optional(u.Job),
		FocusStages: u.FocusStages, Memberships: memberships(u.Memberships),
		Identities: []string{}, CreatedAt: u.CreatedAt, LastLoginAt: s.LastLoginAt,
	}
	if u.Email != "" {
		out.EmailVerified = &verified
	}
	if s.Methods != nil {
		out.Identities = s.Methods
	}
	return out
}

func me(u access.User) httpapi.Me {
	return httpapi.Me{
		Id: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: email(u.Email),
		Status: httpapi.UserStatus(u.Status), PlatformAdmin: u.Admin, Job: optional(u.Job),
		FocusStages: u.FocusStages, Locale: u.Locale, Memberships: memberships(u.Memberships),
	}
}

// ListUsers implements httpapi.StrictServerInterface.
func (h *Handlers) ListUsers(ctx context.Context, req httpapi.ListUsersRequestObject) (httpapi.ListUsersResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.ListUsersResponseObject {
		return httpapi.ListUsersdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	var f access.UserFilter
	if req.Params.Q != nil {
		f.Query = *req.Params.Q
	}
	if req.Params.Status != nil {
		f.Status = access.Status(*req.Params.Status)
	}
	us, err := h.svc.ListUsers(ctx, a, f)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	sis, err := h.signIns(ctx, us)
	if err != nil {
		return nil, err
	}
	out := httpapi.ListUsers200JSONResponse{Items: make([]httpapi.User, len(us))}
	for i, u := range us {
		out.Items[i] = user(u, sis[u.ID])
	}
	return out, nil
}

// GetUser implements httpapi.StrictServerInterface.
func (h *Handlers) GetUser(ctx context.Context, req httpapi.GetUserRequestObject) (httpapi.GetUserResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.GetUserResponseObject {
		return httpapi.GetUserdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	u, err := h.svc.GetUser(ctx, a, req.UserId)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	sis, err := h.signIns(ctx, []access.User{u})
	if err != nil {
		return nil, err
	}
	return httpapi.GetUser200JSONResponse(user(u, sis[u.ID])), nil
}

// UpdateUser implements httpapi.StrictServerInterface.
func (h *Handlers) UpdateUser(ctx context.Context, req httpapi.UpdateUserRequestObject) (httpapi.UpdateUserResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.UpdateUserResponseObject {
		return httpapi.UpdateUserdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	if req.Body == nil {
		return fail(ctx, errNoBody, wrap)
	}
	c := access.UserChange{Admin: req.Body.PlatformAdmin}
	if req.Body.Status != nil {
		st := access.Status(*req.Body.Status)
		c.Status = &st
	}
	u, err := h.svc.UpdateUser(ctx, a, req.UserId, c)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	sis, err := h.signIns(ctx, []access.User{u})
	if err != nil {
		return nil, err
	}
	return httpapi.UpdateUser200JSONResponse(user(u, sis[u.ID])), nil
}

// GetMe implements httpapi.StrictServerInterface.
func (h *Handlers) GetMe(ctx context.Context, _ httpapi.GetMeRequestObject) (httpapi.GetMeResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.GetMeResponseObject {
		return httpapi.GetMedefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	u, err := h.svc.Me(ctx, a)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	return httpapi.GetMe200JSONResponse(me(u)), nil
}

// UpdateMe implements httpapi.StrictServerInterface.
func (h *Handlers) UpdateMe(ctx context.Context, req httpapi.UpdateMeRequestObject) (httpapi.UpdateMeResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.UpdateMeResponseObject {
		return httpapi.UpdateMedefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	if req.Body == nil {
		return fail(ctx, errNoBody, wrap)
	}
	u, err := h.svc.UpdateProfile(ctx, a, access.ProfileChange{
		DisplayName: req.Body.DisplayName, Job: req.Body.Job, FocusStages: req.Body.FocusStages, Locale: req.Body.Locale,
	})
	if err != nil {
		return fail(ctx, err, wrap)
	}
	return httpapi.UpdateMe200JSONResponse(me(u)), nil
}
