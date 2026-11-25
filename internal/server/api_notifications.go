// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/notify"
)

// notificationsAPI implements listMyNotifications and markMyNotificationsRead: the caller's
// own in-app notifications, which package notify writes from events and renders when they are
// read, in the reader's language.
type notificationsAPI struct {
	notify *notify.Service // nil while nothing runs in the background: 501
	access *access.Service
}

func notificationsProblem(err error) error {
	switch {
	case errors.Is(err, notify.ErrBadCursor):
		return httpapi.NewProblem(http.StatusBadRequest, httpapi.CodeRequestInvalid, "The cursor is not one this server gave out.")
	case errors.Is(err, notify.ErrBadLimit), errors.Is(err, notify.ErrTooMany):
		return httpapi.NewProblem(http.StatusBadRequest, httpapi.CodeRequestInvalid, err.Error())
	}
	return err
}

// locale is the language a person reads notifications in: their own setting, otherwise the
// server's default (notify's renderer falls back to it for "").
func (n *notificationsAPI) locale(ctx context.Context, userID string) string {
	u, err := n.access.GetUser(ctx, access.System, userID)
	if err != nil {
		return ""
	}
	return u.Locale
}

func (n *notificationsAPI) ListMyNotifications(ctx context.Context, req httpapi.ListMyNotificationsRequestObject) (httpapi.ListMyNotificationsResponseObject, error) {
	caller, ok := callerFrom(ctx)
	if !ok {
		return nil, errSignInRequired
	}
	if n.notify == nil {
		return nil, httpapi.ErrNotImplemented
	}
	var o notify.ListOptions
	if p := req.Params; p.Unread != nil {
		o.UnreadOnly = *p.Unread
	}
	if req.Params.Limit != nil {
		o.Limit = *req.Params.Limit
		if o.Limit == 0 {
			return nil, notificationsProblem(notify.ErrBadLimit)
		}
	}
	if req.Params.Cursor != nil {
		o.Cursor = *req.Params.Cursor
	}
	page, err := n.notify.List(ctx, caller.UserID, n.locale(ctx, caller.UserID), o)
	if err != nil {
		return nil, notificationsProblem(err)
	}
	out := httpapi.ListMyNotifications200JSONResponse{Items: make([]httpapi.Notification, len(page.Items)), Unread: page.Unread}
	for i, it := range page.Items {
		out.Items[i] = httpapi.Notification{Id: it.ID, Kind: it.Kind, Title: it.Title, CreatedAt: it.CreatedAt, ReadAt: it.ReadAt}
		if it.Body != "" {
			out.Items[i].Body = &it.Body
		}
		if it.Link != "" {
			out.Items[i].Link = &it.Link
		}
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	return out, nil
}

func (n *notificationsAPI) MarkMyNotificationsRead(ctx context.Context, req httpapi.MarkMyNotificationsReadRequestObject) (httpapi.MarkMyNotificationsReadResponseObject, error) {
	caller, ok := callerFrom(ctx)
	if !ok {
		return nil, errSignInRequired
	}
	if n.notify == nil {
		return nil, httpapi.ErrNotImplemented
	}
	body := req.Body
	all := body != nil && body.All != nil && *body.All
	if body == nil || (!all && body.Ids == nil) || (all && body.Ids != nil) {
		return nil, httpapi.NewProblem(http.StatusBadRequest, httpapi.CodeRequestInvalid, "Give either ids or all.")
	}
	var err error
	if all {
		_, err = n.notify.MarkAllRead(ctx, caller.UserID)
	} else {
		_, err = n.notify.MarkRead(ctx, caller.UserID, *body.Ids)
	}
	if err != nil {
		return nil, notificationsProblem(err)
	}
	return httpapi.MarkMyNotificationsRead204Response{}, nil
}
