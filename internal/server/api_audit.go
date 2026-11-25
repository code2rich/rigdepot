// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

// auditAPI implements the operations tagged audit: search and export.
type auditAPI struct {
	log   *audit.Log
	hooks *hooks
}

// auditNames turns the ids in audit entries into what people read. Users
// and devices belong to access and authn.
type auditNames interface {
	// User returns a user's account and display name; false when unknown.
	User(ctx context.Context, id string) (username, displayName string, ok bool)
	// Device returns a computer's name; false when unknown.
	Device(ctx context.Context, id string) (name string, ok bool)
}

// The actions of the audit page, for permission checks and the export's own
// entry.
const (
	actionAuditRead   = "audit.read"
	actionAuditExport = "audit.export"
)

// auditParams are the filters listAudit and exportAudit share.
type auditParams struct {
	Category   *httpapi.AuditCategory
	Actor      *string
	Action     *string
	ObjectKind *string
	ObjectID   *string
	Result     *httpapi.AuditResult
	Since      *time.Time
	Until      *time.Time
}

// filter turns the parameters into a filter and applies what the caller may
// see: platform administrators everything; someone allowed to read the log
// of the object asked for (a resource's maintainers) that object's entries;
// everyone else what they did and what was done to them.
func (a *auditAPI) filter(ctx context.Context, actor audit.Actor, p auditParams) (audit.Filter, error) {
	var f audit.Filter
	if p.Category != nil {
		f.Category = audit.Category(*p.Category)
	}
	if p.Actor != nil {
		f.ActorID = *p.Actor
		if f.ActorID == "me" {
			f.ActorID = actor.UserID
		}
	}
	if p.Action != nil {
		f.Action = *p.Action
	}
	if p.ObjectKind != nil {
		f.ObjectKind = *p.ObjectKind
	}
	if p.ObjectID != nil {
		f.ObjectID = *p.ObjectID
	}
	if p.Result != nil {
		f.Result = audit.Result(*p.Result)
	}
	if p.Since != nil {
		f.Since = *p.Since
	}
	if p.Until != nil {
		f.Until = *p.Until
	}
	may := func(object audit.Object) (bool, error) {
		if a.hooks.authorize == nil {
			return false, nil
		}
		return a.hooks.authorize(ctx, actor, actionAuditRead, object)
	}
	all, err := may(audit.Object{})
	if err != nil || all {
		return f, err
	}
	if f.ObjectKind != "" && f.ObjectID != "" {
		ok, err := may(audit.Object{Kind: f.ObjectKind, ID: f.ObjectID})
		if err != nil || ok {
			return f, err
		}
	}
	f.Concerning = actor.UserID
	return f, nil
}

func (a *auditAPI) ListAudit(ctx context.Context, req httpapi.ListAuditRequestObject) (httpapi.ListAuditResponseObject, error) {
	actor, ok := callerFrom(ctx)
	if !ok {
		return nil, errSignInRequired
	}
	q := req.Params
	f, err := a.filter(ctx, actor, auditParams{q.Category, q.Actor, q.Action, q.ObjectKind, q.ObjectId, q.Result, q.Since, q.Until})
	if err != nil {
		return nil, err
	}
	limit := 0
	if q.Limit != nil {
		if *q.Limit < 1 || *q.Limit > audit.MaxLimit {
			return nil, fieldProblem("limit", "invalid")
		}
		limit = *q.Limit
	}
	cursor := ""
	if q.Cursor != nil {
		cursor = *q.Cursor
	}
	page, err := a.log.Query(ctx, f, limit, cursor)
	if errors.Is(err, audit.ErrBadCursor) {
		return nil, fieldProblem("cursor", "invalid")
	}
	if err != nil {
		return nil, err
	}
	out := httpapi.ListAudit200JSONResponse{Items: make([]httpapi.AuditEntry, 0, len(page.Rows))}
	for _, r := range page.Rows {
		out.Items = append(out.Items, a.entry(ctx, r))
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	return out, nil
}

func (a *auditAPI) entry(ctx context.Context, r audit.Row) httpapi.AuditEntry {
	e := httpapi.AuditEntry{
		Seq: int(r.Seq), At: r.At, Category: httpapi.AuditCategory(r.Category), Origin: httpapi.AuditEntryOrigin(r.Actor.Origin),
		Action: r.Action, ActionLabel: r.Action, // TODO(i18n): translated action names once the server's catalogue exists
		Object: httpapi.AuditObject{Kind: r.Object.Kind, Id: r.Object.ID, Label: r.Object.Label},
		Change: map[string]any{}, Result: httpapi.AuditResult(r.Result),
	}
	if err := json.Unmarshal([]byte(r.Change), &e.Change); err != nil {
		e.Change = map[string]any{}
	}
	if id := r.Actor.UserID; id != "" {
		ref := httpapi.UserRef{Id: id, Username: id}
		if a.hooks.names != nil {
			if username, display, ok := a.hooks.names.User(ctx, id); ok {
				ref.Username, ref.DisplayName = username, display
			}
		}
		e.Actor = &ref
	}
	if id := r.Actor.DeviceID; id != "" {
		dev := httpapi.AuditDevice{Id: id, Name: id}
		if a.hooks.names != nil {
			if name, ok := a.hooks.names.Device(ctx, id); ok {
				dev.Name = name
			}
		}
		e.Device = &dev
	}
	if r.Actor.ViaTool != "" {
		e.ViaTool = &r.Actor.ViaTool
	}
	if r.Method != "" {
		e.Method = &r.Method
	}
	if r.Reason != "" {
		e.Reason = &r.Reason
	}
	return e
}

func (a *auditAPI) ExportAudit(ctx context.Context, req httpapi.ExportAuditRequestObject) (httpapi.ExportAuditResponseObject, error) {
	actor, ok := callerFrom(ctx)
	if !ok {
		return nil, errSignInRequired
	}
	q := req.Params
	f, err := a.filter(ctx, actor, auditParams{q.Category, q.Actor, q.Action, q.ObjectKind, q.ObjectId, q.Result, q.Since, q.Until})
	if err != nil {
		return nil, err
	}
	// The export is itself an entry, written before the rows are read.
	change := map[string]any{}
	if f.Concerning != "" {
		change["scope"] = "own"
	}
	for k, v := range map[string]string{"category": string(f.Category), "actor": f.ActorID, "action": f.Action,
		"object_kind": f.ObjectKind, "object_id": f.ObjectID, "result": string(f.Result)} {
		if v != "" {
			change[k] = v
		}
	}
	if _, err := a.log.Record(ctx, audit.Entry{Actor: actor, Action: actionAuditExport,
		Object: audit.Object{Kind: "audit_log", ID: "audit_log", Label: "Audit log"}, Change: change}); err != nil {
		return nil, err
	}
	rows, err := a.log.Scan(ctx, f)
	if err != nil {
		return nil, err
	}
	return csvExport{rows: rows, names: a.exportNames(ctx)}, nil
}

func (a *auditAPI) exportNames(ctx context.Context) audit.Names {
	if a.hooks.names == nil {
		return audit.Names{}
	}
	users, devices := map[string]string{}, map[string]string{}
	return audit.Names{
		User: func(id string) string {
			if n, ok := users[id]; ok {
				return n
			}
			n, _, _ := a.hooks.names.User(ctx, id)
			users[id] = n
			return n
		},
		Device: func(id string) string {
			if n, ok := devices[id]; ok {
				return n
			}
			n, _ := a.hooks.names.Device(ctx, id)
			devices[id] = n
			return n
		},
	}
}

// csvExport streams the rows straight into the response, so that a large
// log is never held in memory.
type csvExport struct {
	rows  *audit.Rows
	names audit.Names
}

func (c csvExport) VisitExportAuditResponse(w http.ResponseWriter) error {
	defer c.rows.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	return audit.WriteCSV(w, c.rows, c.names)
}

func fieldProblem(field, code string) error {
	p := httpapi.NewProblem(http.StatusBadRequest, httpapi.CodeRequestInvalid, "The request has invalid parameters.")
	p.Problem.Errors = &[]httpapi.FieldError{{Field: field, Code: code}}
	return p
}
