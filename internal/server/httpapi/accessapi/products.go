// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package accessapi

import (
	"context"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

func (h *Handlers) resourceCounts(ctx context.Context) (map[string]int, error) {
	if h.o.ResourceCounts == nil {
		return map[string]int{}, nil
	}
	return h.o.ResourceCounts(ctx)
}

func product(p access.Product, counts map[string]int) httpapi.Product {
	out := httpapi.Product{
		Code: p.Code, Name: p.Name, Line: optional(p.Line),
		Owners: make([]httpapi.UserRef, len(p.Owners)), MemberCount: p.MemberCount,
		ResourceCount: counts[p.ID],
	}
	for i, o := range p.Owners {
		out.Owners[i] = userRef(o)
	}
	if p.MyRole != "" {
		r := httpapi.Role(p.MyRole)
		out.MyRole = &r
	}
	return out
}

func member(m access.Member) httpapi.Member {
	stages := m.FocusStages
	return httpapi.Member{User: userRef(m.User), Role: httpapi.Role(m.Role), Job: optional(m.Job), FocusStages: &stages, AddedAt: m.AddedAt}
}

// ListProducts implements httpapi.StrictServerInterface.
func (h *Handlers) ListProducts(ctx context.Context, _ httpapi.ListProductsRequestObject) (httpapi.ListProductsResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.ListProductsResponseObject {
		return httpapi.ListProductsdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	ps, err := h.svc.ListProducts(ctx, a)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	counts, err := h.resourceCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := httpapi.ListProducts200JSONResponse{Items: make([]httpapi.Product, len(ps))}
	for i, p := range ps {
		out.Items[i] = product(p, counts)
	}
	return out, nil
}

// CreateProduct implements httpapi.StrictServerInterface.
func (h *Handlers) CreateProduct(ctx context.Context, req httpapi.CreateProductRequestObject) (httpapi.CreateProductResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.CreateProductResponseObject {
		return httpapi.CreateProductdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	if req.Body == nil {
		return fail(ctx, errNoBody, wrap)
	}
	n := access.NewProduct{Code: req.Body.Code, Name: req.Body.Name}
	if req.Body.Line != nil {
		n.Line = *req.Body.Line
	}
	if req.Body.Owners != nil {
		n.Owners = *req.Body.Owners
	}
	p, err := h.svc.CreateProduct(ctx, a, n)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	counts, err := h.resourceCounts(ctx)
	if err != nil {
		return nil, err
	}
	return httpapi.CreateProduct201JSONResponse(product(p, counts)), nil
}

// GetProduct implements httpapi.StrictServerInterface.
func (h *Handlers) GetProduct(ctx context.Context, req httpapi.GetProductRequestObject) (httpapi.GetProductResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.GetProductResponseObject {
		return httpapi.GetProductdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	p, err := h.svc.GetProduct(ctx, a, req.Product)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	counts, err := h.resourceCounts(ctx)
	if err != nil {
		return nil, err
	}
	return httpapi.GetProduct200JSONResponse(product(p, counts)), nil
}

// UpdateProduct implements httpapi.StrictServerInterface.
func (h *Handlers) UpdateProduct(ctx context.Context, req httpapi.UpdateProductRequestObject) (httpapi.UpdateProductResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.UpdateProductResponseObject {
		return httpapi.UpdateProductdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	if req.Body == nil {
		return fail(ctx, errNoBody, wrap)
	}
	p, err := h.svc.UpdateProduct(ctx, a, req.Product, access.ProductChange{Name: req.Body.Name, Line: req.Body.Line})
	if err != nil {
		return fail(ctx, err, wrap)
	}
	counts, err := h.resourceCounts(ctx)
	if err != nil {
		return nil, err
	}
	return httpapi.UpdateProduct200JSONResponse(product(p, counts)), nil
}

// ListProductMembers implements httpapi.StrictServerInterface.
func (h *Handlers) ListProductMembers(ctx context.Context, req httpapi.ListProductMembersRequestObject) (httpapi.ListProductMembersResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.ListProductMembersResponseObject {
		return httpapi.ListProductMembersdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	ms, err := h.svc.ListMembers(ctx, a, req.Product)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	out := httpapi.ListProductMembers200JSONResponse{Items: make([]httpapi.Member, len(ms))}
	for i, m := range ms {
		out.Items[i] = member(m)
	}
	return out, nil
}

// PutProductMember implements httpapi.StrictServerInterface.
func (h *Handlers) PutProductMember(ctx context.Context, req httpapi.PutProductMemberRequestObject) (httpapi.PutProductMemberResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.PutProductMemberResponseObject {
		return httpapi.PutProductMemberdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	if req.Body == nil {
		return fail(ctx, errNoBody, wrap)
	}
	m, added, err := h.svc.PutMember(ctx, a, req.Product, req.UserId, access.Role(req.Body.Role))
	if err != nil {
		return fail(ctx, err, wrap)
	}
	if added {
		return httpapi.PutProductMember201JSONResponse(member(m)), nil
	}
	return httpapi.PutProductMember200JSONResponse(member(m)), nil
}

// RemoveProductMember implements httpapi.StrictServerInterface.
func (h *Handlers) RemoveProductMember(ctx context.Context, req httpapi.RemoveProductMemberRequestObject) (httpapi.RemoveProductMemberResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.RemoveProductMemberResponseObject {
		return httpapi.RemoveProductMemberdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	if err := h.svc.RemoveMember(ctx, a, req.Product, req.UserId); err != nil {
		return fail(ctx, err, wrap)
	}
	return httpapi.RemoveProductMember204Response{}, nil
}
