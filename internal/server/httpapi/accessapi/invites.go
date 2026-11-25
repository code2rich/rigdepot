// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package accessapi

import (
	"context"
	"net/url"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

// inviteLink is the console page an invitation link opens
// (docs/architecture/07-web-console.md: /invite/<token>).
func (h *Handlers) inviteLink(token string) string {
	base := url.URL{Path: "/"}
	if h.o.BaseURL != nil {
		base = *h.o.BaseURL
	}
	return base.JoinPath("invite", token).String()
}

func invite(inv access.Invite) httpapi.Invite {
	out := httpapi.Invite{
		Id: inv.ID, Email: openapi_types.Email(inv.Email), Products: make([]httpapi.ProductRef, len(inv.Products)),
		CreatedBy: userRef(inv.CreatedBy), CreatedAt: inv.CreatedAt, ExpiresAt: inv.ExpiresAt,
	}
	for i, p := range inv.Products {
		out.Products[i] = productRef(p)
	}
	return out
}

// ListInvites implements httpapi.StrictServerInterface.
func (h *Handlers) ListInvites(ctx context.Context, _ httpapi.ListInvitesRequestObject) (httpapi.ListInvitesResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.ListInvitesResponseObject {
		return httpapi.ListInvitesdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	invs, err := h.svc.ListInvites(ctx, a)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	out := httpapi.ListInvites200JSONResponse{Items: make([]httpapi.Invite, len(invs))}
	for i, inv := range invs {
		out.Items[i] = invite(inv)
	}
	return out, nil
}

// CreateInvites implements httpapi.StrictServerInterface. Each link is
// mailed when mail is set up; otherwise, and when sending fails, it is in the
// response, the only time anyone sees it.
func (h *Handlers) CreateInvites(ctx context.Context, req httpapi.CreateInvitesRequestObject) (httpapi.CreateInvitesResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.CreateInvitesResponseObject {
		return httpapi.CreateInvitesdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	if req.Body == nil {
		return fail(ctx, errNoBody, wrap)
	}
	emails := make([]string, len(req.Body.Emails))
	for i, e := range req.Body.Emails {
		emails[i] = string(e)
	}
	var products []string
	if req.Body.Products != nil {
		products = *req.Body.Products
	}
	invs, err := h.svc.CreateInvites(ctx, a, emails, products)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	out := httpapi.CreateInvites201JSONResponse{Items: make([]httpapi.Invite, len(invs))}
	for i, inv := range invs {
		out.Items[i] = invite(inv)
		link := h.inviteLink(inv.Token)
		if h.o.MailInvite == nil || h.o.MailInvite(ctx, inv, link) != nil {
			out.Items[i].Link = &link
		}
	}
	return out, nil
}

// RevokeInvite implements httpapi.StrictServerInterface.
func (h *Handlers) RevokeInvite(ctx context.Context, req httpapi.RevokeInviteRequestObject) (httpapi.RevokeInviteResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.RevokeInviteResponseObject {
		return httpapi.RevokeInvitedefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	if err := h.svc.RevokeInvite(ctx, a, req.InviteId); err != nil {
		return fail(ctx, err, wrap)
	}
	return httpapi.RevokeInvite204Response{}, nil
}

// GetInvite implements httpapi.StrictServerInterface. It needs no sign-in:
// the token is the credential.
func (h *Handlers) GetInvite(ctx context.Context, req httpapi.GetInviteRequestObject) (httpapi.GetInviteResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.GetInviteResponseObject {
		return httpapi.GetInvitedefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	inv, err := h.svc.PreviewInvite(ctx, req.InviteToken)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	out := httpapi.GetInvite200JSONResponse{
		Email: openapi_types.Email(inv.Email), ExpiresAt: inv.ExpiresAt, Products: make([]httpapi.ProductRef, len(inv.Products)),
	}
	for i, p := range inv.Products {
		out.Products[i] = productRef(p)
	}
	return out, nil
}
