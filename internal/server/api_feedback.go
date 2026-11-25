// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/feedback"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

// feedbackAPI implements the operations of the feedback queue (tag feedback). The rules (who
// sees and handles what, the states, the context's fields, scores) are in package feedback.
type feedbackAPI struct {
	fb    *feedback.Service
	hooks *hooks
}

// feedbackDetails are the English texts of the queue's problems.
// TODO(i18n): render them in the request's language once locales/*/server.json has them.
var feedbackDetails = map[string]string{
	"access.denied":            "You are not allowed to do this.",
	"object.not_found":         "Not found, or not visible to you.",
	"request.invalid":          "The request has invalid fields.",
	"feedback.reason_required": "Say why it will not be fixed.",
	"feedback.not_resolved":    "Only resolved feedback can be rated.",
}

var feedbackStatus = map[feedback.Kind]int{
	feedback.KindInvalid:  http.StatusBadRequest,
	feedback.KindDenied:   http.StatusForbidden,
	feedback.KindNotFound: http.StatusNotFound,
	feedback.KindConflict: http.StatusConflict,
}

// feedbackError turns the queue's errors into problem documents; others pass on as faults.
func feedbackError(ctx context.Context, err error) error {
	var e *feedback.Error
	if !errors.As(err, &e) {
		return err
	}
	status, ok := feedbackStatus[e.Kind]
	if !ok {
		return err
	}
	p := httpapi.NewProblem(status, e.Code, feedbackDetails[e.Code])
	if len(e.Fields) > 0 {
		fields := make([]httpapi.FieldError, len(e.Fields))
		for i, f := range e.Fields {
			fields[i] = httpapi.FieldError{Field: f.Field, Code: f.Code}
			if f.Detail != "" {
				fields[i].Detail = &f.Detail
			}
		}
		p.Problem.Errors = &fields
	}
	if id := logging.RequestID(ctx); id != "" {
		p.Problem.RequestId = &id
	}
	return p
}

func (h *feedbackAPI) userRef(ctx context.Context, id string) *httpapi.UserRef {
	if id == "" {
		return nil
	}
	ref := httpapi.UserRef{Id: id, Username: id, DisplayName: id}
	if h.hooks.names != nil {
		if u, d, ok := h.hooks.names.User(ctx, id); ok {
			ref.Username, ref.DisplayName = u, d
		}
	}
	return &ref
}

func optInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func contextOut(c feedback.Context) httpapi.FeedbackContext {
	out := httpapi.FeedbackContext{Page: optStr(c.Page), Browser: optStr(c.Browser), ToolVersion: optStr(c.ToolVersion),
		CliVersion: optStr(c.CLIVersion), Os: optStr(c.OS), Trigger: optStr(c.Trigger)}
	if c.Tool != "" {
		out.Tool = &c.Tool
	}
	if c.LastSync != "" {
		v := httpapi.SyncResult(c.LastSync)
		out.LastSync = &v
	}
	if len(c.Frictions) > 0 {
		fs := make([]httpapi.FrictionCount, len(c.Frictions))
		for i, f := range c.Frictions {
			fs[i] = httpapi.FrictionCount{Type: httpapi.FrictionCountType(f.Type), Count: f.Count}
		}
		out.Frictions = &fs
	}
	return out
}

func contextIn(c *httpapi.FeedbackContext) feedback.Context {
	if c == nil {
		return feedback.Context{}
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	out := feedback.Context{Page: str(c.Page), Browser: str(c.Browser), Tool: str(c.Tool), ToolVersion: str(c.ToolVersion),
		CLIVersion: str(c.CliVersion), OS: str(c.Os), Trigger: str(c.Trigger)}
	if c.LastSync != nil {
		out.LastSync = string(*c.LastSync)
	}
	if c.Frictions != nil {
		for _, f := range *c.Frictions {
			out.Frictions = append(out.Frictions, feedback.FrictionCount{Type: string(f.Type), Count: f.Count})
		}
	}
	return out
}

func (h *feedbackAPI) eventOut(ctx context.Context, e feedback.Event) httpapi.FeedbackEvent {
	out := httpapi.FeedbackEvent{Id: e.ID, Kind: httpapi.FeedbackEventKind(e.Kind), By: h.userRef(ctx, e.ActorID), Body: optStr(e.Body),
		Assignee: h.userRef(ctx, e.AssigneeID), Score: optInt(e.Score), At: e.At}
	if e.Status != "" {
		st := httpapi.FeedbackStatus(e.Status)
		out.Status = &st
	}
	return out
}

func resourceRefOut(r *feedback.ResourceRef) *httpapi.ResourceRef {
	if r == nil {
		return nil
	}
	return &httpapi.ResourceRef{Code: r.Code, Kind: r.Kind, Name: r.Name}
}

func (h *feedbackAPI) itemOut(ctx context.Context, f feedback.Feedback) httpapi.FeedbackListItem {
	return httpapi.FeedbackListItem{Number: f.Number, Title: f.Title, Type: httpapi.FeedbackType(f.Type), Origin: httpapi.FeedbackOrigin(f.Origin),
		Status: httpapi.FeedbackStatus(f.Status), Severity: httpapi.FeedbackSeverity(f.Severity), Submitter: h.userRef(ctx, f.SubmitterID),
		Assignee: h.userRef(ctx, f.AssigneeID), Resource: resourceRefOut(f.Resource), ResourceVersion: optInt(f.ResourceVersion),
		Score: optInt(f.Score), CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt}
}

func (h *feedbackAPI) feedbackOut(ctx context.Context, f feedback.Feedback) httpapi.Feedback {
	out := httpapi.Feedback{Number: f.Number, Title: f.Title, Type: httpapi.FeedbackType(f.Type), Origin: httpapi.FeedbackOrigin(f.Origin),
		Status: httpapi.FeedbackStatus(f.Status), Severity: httpapi.FeedbackSeverity(f.Severity), Submitter: h.userRef(ctx, f.SubmitterID),
		Assignee: h.userRef(ctx, f.AssigneeID), Resource: resourceRefOut(f.Resource), ResourceVersion: optInt(f.ResourceVersion),
		Score: optInt(f.Score), ScoreNote: optStr(f.ScoreNote), StatusReason: optStr(f.StatusReason), CreatedAt: f.CreatedAt,
		UpdatedAt: f.UpdatedAt, Body: f.Body, Context: contextOut(f.Context), Events: make([]httpapi.FeedbackEvent, 0, len(f.Events))}
	for _, e := range f.Events {
		out.Events = append(out.Events, h.eventOut(ctx, e))
	}
	return out
}

func (h *feedbackAPI) ListFeedback(ctx context.Context, req httpapi.ListFeedbackRequestObject) (httpapi.ListFeedbackResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	p := req.Params
	var f feedback.Filter
	if p.Status != nil {
		f.Status = feedback.Status(*p.Status)
	}
	if p.Type != nil {
		f.Type = feedback.Type(*p.Type)
	}
	if p.Resource != nil {
		f.Resource = *p.Resource
	}
	f.Mine = p.Mine != nil && *p.Mine
	f.AssignedToMe = p.AssignedToMe != nil && *p.AssignedToMe
	limit, cursor := 0, ""
	if p.Limit != nil {
		limit = *p.Limit
	}
	if p.Cursor != nil {
		cursor = *p.Cursor
	}
	list, next, err := h.fb.List(ctx, a, f, limit, cursor)
	if err != nil {
		return nil, feedbackError(ctx, err)
	}
	out := httpapi.FeedbackPage{Items: make([]httpapi.FeedbackListItem, 0, len(list)), NextCursor: optStr(next)}
	for _, fb := range list {
		out.Items = append(out.Items, h.itemOut(ctx, fb))
	}
	return httpapi.ListFeedback200JSONResponse(out), nil
}

func (h *feedbackAPI) CreateFeedback(ctx context.Context, req httpapi.CreateFeedbackRequestObject) (httpapi.CreateFeedbackResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem("body", "required")
	}
	b := req.Body
	n := feedback.Submission{Type: feedback.Type(b.Type), Title: b.Title, Body: b.Body, AIDrafted: b.AiDrafted != nil && *b.AiDrafted,
		Context: contextIn(b.Context)}
	if b.Severity != nil {
		n.Severity = feedback.Severity(*b.Severity)
	}
	if b.Resource != nil {
		n.Resource = *b.Resource
	}
	if b.ResourceVersion != nil {
		n.ResourceVersion = *b.ResourceVersion
	}
	fb, err := h.fb.Create(ctx, a, n)
	if err != nil {
		return nil, feedbackError(ctx, err)
	}
	return httpapi.CreateFeedback201JSONResponse(h.feedbackOut(ctx, fb)), nil
}

func (h *feedbackAPI) GetFeedbackSummary(ctx context.Context, req httpapi.GetFeedbackSummaryRequestObject) (httpapi.GetFeedbackSummaryResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	s, err := h.fb.Summary(ctx, a, req.Params.Mine != nil && *req.Params.Mine)
	if err != nil {
		return nil, feedbackError(ctx, err)
	}
	var out httpapi.FeedbackSummary
	out.Open = s.Open
	out.ByStatus.New = s.ByStatus[feedback.StatusNew]
	out.ByStatus.Accepted = s.ByStatus[feedback.StatusAccepted]
	out.ByStatus.InProgress = s.ByStatus[feedback.StatusInProgress]
	out.ByStatus.Resolved = s.ByStatus[feedback.StatusResolved]
	out.ByStatus.WontFix = s.ByStatus[feedback.StatusWontFix]
	return httpapi.GetFeedbackSummary200JSONResponse(out), nil
}

func (h *feedbackAPI) GetFeedback(ctx context.Context, req httpapi.GetFeedbackRequestObject) (httpapi.GetFeedbackResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	fb, err := h.fb.Get(ctx, a, req.Number)
	if err != nil {
		return nil, feedbackError(ctx, err)
	}
	return httpapi.GetFeedback200JSONResponse(h.feedbackOut(ctx, fb)), nil
}

func (h *feedbackAPI) UpdateFeedback(ctx context.Context, req httpapi.UpdateFeedbackRequestObject) (httpapi.UpdateFeedbackResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem("body", "required")
	}
	b := req.Body
	c := feedback.Change{StatusReason: b.StatusReason, Assignee: b.Assignee, Unassign: b.Unassign != nil && *b.Unassign}
	if b.Status != nil {
		st := feedback.Status(*b.Status)
		c.Status = &st
	}
	if b.Severity != nil {
		sv := feedback.Severity(*b.Severity)
		c.Severity = &sv
	}
	fb, err := h.fb.Update(ctx, a, req.Number, c)
	if err != nil {
		return nil, feedbackError(ctx, err)
	}
	return httpapi.UpdateFeedback200JSONResponse(h.feedbackOut(ctx, fb)), nil
}

func (h *feedbackAPI) CommentOnFeedback(ctx context.Context, req httpapi.CommentOnFeedbackRequestObject) (httpapi.CommentOnFeedbackResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem("body", "required")
	}
	e, err := h.fb.Comment(ctx, a, req.Number, req.Body.Body)
	if err != nil {
		return nil, feedbackError(ctx, err)
	}
	return httpapi.CommentOnFeedback201JSONResponse(h.eventOut(ctx, e)), nil
}

func (h *feedbackAPI) RateFeedback(ctx context.Context, req httpapi.RateFeedbackRequestObject) (httpapi.RateFeedbackResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem("body", "required")
	}
	note := ""
	if req.Body.Note != nil {
		note = *req.Body.Note
	}
	fb, err := h.fb.Rate(ctx, a, req.Number, req.Body.Score, note)
	if err != nil {
		return nil, feedbackError(ctx, err)
	}
	return httpapi.RateFeedback200JSONResponse(h.feedbackOut(ctx, fb)), nil
}
