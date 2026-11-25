// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package accessapi

import (
	"context"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

// joinRequests converts requests, with the sign-in method each person came
// through when it is known.
func (h *Handlers) joinRequests(ctx context.Context, js []access.JoinRequest) ([]httpapi.JoinRequest, error) {
	sis := map[string]SignIn{}
	if h.o.SignIns != nil && len(js) > 0 {
		var ids []string
		for _, j := range js {
			ids = append(ids, j.User.ID)
		}
		var err error
		if sis, err = h.o.SignIns(ctx, ids); err != nil {
			return nil, err
		}
	}
	out := make([]httpapi.JoinRequest, len(js))
	for i, j := range js {
		stages := j.FocusStages
		r := httpapi.JoinRequest{
			Id: j.ID, User: userRef(j.User), Job: optional(j.Job), FocusStages: &stages,
			Product: productRef(j.Product), Note: optional(j.Note), Status: httpapi.JoinRequestStatus(j.Status),
			CreatedAt: j.CreatedAt, DecidedAt: j.DecidedAt, Reason: optional(j.Reason),
		}
		if j.DecidedBy != nil {
			d := userRef(*j.DecidedBy)
			r.DecidedBy = &d
		}
		if m := sis[j.User.ID].Methods; len(m) > 0 {
			r.Identity = &m[0]
		}
		out[i] = r
	}
	return out, nil
}

// ListMyJoinRequests implements httpapi.StrictServerInterface.
func (h *Handlers) ListMyJoinRequests(ctx context.Context, _ httpapi.ListMyJoinRequestsRequestObject) (httpapi.ListMyJoinRequestsResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.ListMyJoinRequestsResponseObject {
		return httpapi.ListMyJoinRequestsdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	js, err := h.svc.MyJoinRequests(ctx, a)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	items, err := h.joinRequests(ctx, js)
	if err != nil {
		return nil, err
	}
	return httpapi.ListMyJoinRequests200JSONResponse{Items: items}, nil
}

// CreateJoinRequests implements httpapi.StrictServerInterface.
func (h *Handlers) CreateJoinRequests(ctx context.Context, req httpapi.CreateJoinRequestsRequestObject) (httpapi.CreateJoinRequestsResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.CreateJoinRequestsResponseObject {
		return httpapi.CreateJoinRequestsdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	if req.Body == nil {
		return fail(ctx, errNoBody, wrap)
	}
	note := ""
	if req.Body.Note != nil {
		note = *req.Body.Note
	}
	js, err := h.svc.RequestToJoin(ctx, a, req.Body.Products, note)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	items, err := h.joinRequests(ctx, js)
	if err != nil {
		return nil, err
	}
	return httpapi.CreateJoinRequests201JSONResponse{Items: items}, nil
}

// ListJoinRequests implements httpapi.StrictServerInterface.
func (h *Handlers) ListJoinRequests(ctx context.Context, req httpapi.ListJoinRequestsRequestObject) (httpapi.ListJoinRequestsResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.ListJoinRequestsResponseObject {
		return httpapi.ListJoinRequestsdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	var f access.JoinFilter
	if req.Params.Product != nil {
		f.Product = *req.Params.Product
	}
	if req.Params.Status != nil {
		f.Status = access.JoinStatus(*req.Params.Status)
	}
	js, err := h.svc.JoinRequests(ctx, a, f)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	items, err := h.joinRequests(ctx, js)
	if err != nil {
		return nil, err
	}
	return httpapi.ListJoinRequests200JSONResponse{Items: items}, nil
}

// ApproveJoinRequest implements httpapi.StrictServerInterface.
func (h *Handlers) ApproveJoinRequest(ctx context.Context, req httpapi.ApproveJoinRequestRequestObject) (httpapi.ApproveJoinRequestResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.ApproveJoinRequestResponseObject {
		return httpapi.ApproveJoinRequestdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	j, err := h.svc.ApproveJoinRequest(ctx, a, req.RequestId)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	items, err := h.joinRequests(ctx, []access.JoinRequest{j})
	if err != nil {
		return nil, err
	}
	return httpapi.ApproveJoinRequest200JSONResponse(items[0]), nil
}

// RejectJoinRequest implements httpapi.StrictServerInterface.
func (h *Handlers) RejectJoinRequest(ctx context.Context, req httpapi.RejectJoinRequestRequestObject) (httpapi.RejectJoinRequestResponseObject, error) {
	wrap := func(p httpapi.Problem) httpapi.RejectJoinRequestResponseObject {
		return httpapi.RejectJoinRequestdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}
	}
	a, err := actor(ctx)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	if req.Body == nil {
		return fail(ctx, errNoBody, wrap)
	}
	j, err := h.svc.RejectJoinRequest(ctx, a, req.RequestId, req.Body.Reason)
	if err != nil {
		return fail(ctx, err, wrap)
	}
	items, err := h.joinRequests(ctx, []access.JoinRequest{j})
	if err != nil {
		return nil, err
	}
	return httpapi.RejectJoinRequest200JSONResponse(items[0]), nil
}
