// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/delivery"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/httpapi/authnapi"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// deliveryAPI implements the operations of delivery: the desired state, the bundles, the sync
// reports and the summary built from them (tag sync, except the sealed secrets, which are the
// secrets domain's), and a person turning a resource on or off for themselves (tag catalog:
// setCatalogPreference). The rules are in package delivery.
type deliveryAPI struct {
	svc      *delivery.Service
	settings *settings.Service
	tools    *toolsAPI // the command-line versions this server accepts
	hooks    *hooks
}

// mcpModeDefault is the organisation's default way of writing MCP servers into tools. There is
// no setting for it yet; it is the command line's own default (localconfig mcp.mode).
const mcpModeDefault = httpapi.OrgDefaultsMcpMode("single-entry")

var deliveryStatus = map[delivery.Kind]int{
	delivery.KindInvalid:  http.StatusBadRequest,
	delivery.KindDenied:   http.StatusForbidden,
	delivery.KindNotFound: http.StatusNotFound,
	delivery.KindConflict: http.StatusConflict,
}

// deliveryDetails are the English texts of delivery's problems.
// TODO(i18n): render them in the request's language once locales/*/server.json has them.
var deliveryDetails = map[string]string{
	"access.denied":     "You are not allowed to do this.",
	"object.not_found":  "Not found, or not visible to you.",
	"request.invalid":   "The request has invalid fields.",
	"resource.required": "This resource is required in your scope and cannot be turned off.",
}

// deliveryError turns delivery's errors into problem documents; other errors pass on as faults
// (500), except the catalog's, which catalogError maps.
func deliveryError(ctx context.Context, err error) error {
	var e *delivery.Error
	if !errors.As(err, &e) {
		return catalogError(ctx, err)
	}
	status, ok := deliveryStatus[e.Kind]
	if !ok {
		return err
	}
	p := httpapi.NewProblem(status, e.Code, deliveryDetails[e.Code])
	if len(e.Params) > 0 {
		params := e.Params
		p.Problem.Params = &params
	}
	if len(e.Fields) > 0 {
		fields := make([]httpapi.FieldError, len(e.Fields))
		for i, f := range e.Fields {
			fields[i] = httpapi.FieldError{Field: f.Field, Code: f.Code}
		}
		p.Problem.Errors = &fields
	}
	if id := logging.RequestID(ctx); id != "" {
		p.Problem.RequestId = &id
	}
	return p
}

func deliveryCaller(ctx context.Context) (access.Actor, error) {
	a, ok := access.ActorFrom(ctx)
	if !ok || a.UserID == "" {
		return access.Actor{}, errSignInRequired
	}
	return a, nil
}

func (d *deliveryAPI) SetCatalogPreference(ctx context.Context, req httpapi.SetCatalogPreferenceRequestObject) (httpapi.SetCatalogPreferenceResponseObject, error) {
	a, err := deliveryCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem("body", "required")
	}
	p, err := d.svc.SetPreference(ctx, a, req.Code, req.Body.Enabled)
	if errors.Is(err, delivery.ErrRequired) {
		detail := deliveryDetails["resource.required"]
		params := map[string]any{"code": req.Code}
		out := httpapi.SetCatalogPreference409ApplicationProblemPlusJSONResponse{Type: "about:blank", Title: http.StatusText(http.StatusConflict),
			Status: http.StatusConflict, Code: "resource.required", Detail: &detail, Params: &params}
		if id := logging.RequestID(ctx); id != "" {
			out.RequestId = &id
		}
		return out, nil
	}
	if err != nil {
		return nil, deliveryError(ctx, err)
	}
	return httpapi.SetCatalogPreference200JSONResponse{Code: p.Code, Enabled: p.Enabled, Policy: httpapi.Policy(p.Policy)}, nil
}

// desiredMeta is a resource's metadata in force, as the contract writes it.
func desiredMeta(it delivery.Item) httpapi.ResourceMeta {
	m := it.Resource.Meta
	out := httpapi.ResourceMeta{Name: it.Resource.Name, Summary: optStr(it.Resource.Summary), When: optStr(m.When), Try: optStr(m.Try)}
	if len(it.Resource.Stages) > 0 {
		s := slices.Clone(it.Resource.Stages)
		out.Stages = &s
	}
	if it.Resource.Audience != "" {
		v := httpapi.Audience(it.Resource.Audience)
		out.Audience = &v
	}
	if it.Resource.Scope != "" {
		v := string(it.Resource.Scope)
		out.Scope = &v
	}
	if it.Resource.Policy != "" {
		v := httpapi.Policy(it.Resource.Policy)
		out.Policy = &v
	}
	if len(m.Secrets) > 0 {
		s := slices.Clone(m.Secrets)
		out.Secrets = &s
	}
	if len(m.Tags) > 0 {
		s := slices.Clone(m.Tags)
		out.Tags = &s
	}
	if len(m.I18n) > 0 {
		tr := make(map[string]httpapi.ResourceMetaText, len(m.I18n))
		for lang, t := range m.I18n {
			tr[lang] = httpapi.ResourceMetaText{Name: optStr(t.Name), Summary: optStr(t.Summary), When: optStr(t.When), Try: optStr(t.Try)}
		}
		out.I18n = &tr
	}
	return out
}

// desiredETag is the entity tag of a desired state: the digest of everything in it but the time
// it was made, so that it changes exactly when something the command line acts on changes.
func desiredETag(doc httpapi.DesiredState) (string, error) {
	doc.GeneratedAt = time.Time{} // doc is a copy
	data, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return `"d-` + hex.EncodeToString(sum[:16]) + `"`, nil
}

func (d *deliveryAPI) GetDesiredState(ctx context.Context, req httpapi.GetDesiredStateRequestObject) (httpapi.GetDesiredStateResponseObject, error) {
	a, err := deliveryCaller(ctx)
	if err != nil {
		return nil, err
	}
	st, err := d.svc.Desired(ctx, a)
	if err != nil {
		return nil, deliveryError(ctx, err)
	}
	disabled, err := d.settings.DisabledAdapters(ctx)
	if err != nil {
		return nil, err
	}
	latest, minimum, err := d.tools.cliRange(ctx)
	if err != nil {
		return nil, err
	}
	doc := httpapi.DesiredState{
		GeneratedAt: d.svc.Now().UTC(),
		User:        (&catalogAPI{hooks: d.hooks}).userRef(ctx, a.UserID),
		Cli:         httpapi.CLIVersions{LatestVersion: latest, MinVersion: minimum},
		Defaults:    httpapi.OrgDefaults{ToolsDisabled: append([]string{}, disabled...), McpMode: mcpModeDefault},
		Resources:   make([]httpapi.DesiredResource, 0, len(st.Items)),
		Secrets:     make([]httpapi.DesiredSecret, 0, len(st.Secrets)),
	}
	for _, it := range st.Items {
		r := httpapi.DesiredResource{
			Code: it.Resource.Code, Kind: it.Resource.Kind, Version: it.Live.Version, Digest: string(it.Live.Digest),
			Bundle: it.Live.Bundle, Size: int(it.Live.Size), Reason: httpapi.DesiredResourceReason(it.Reason),
			Delivery: mcpDelivery(it.Resource.Meta.Delivery), Meta: desiredMeta(it),
		}
		if len(it.RequiredBy) > 0 {
			by := slices.Clone(it.RequiredBy)
			r.RequiredBy = &by
		}
		doc.Resources = append(doc.Resources, r)
	}
	configured := map[string]bool{}
	if f := d.hooks.secretsConfigured; f != nil && len(st.Secrets) > 0 {
		if configured, err = f(ctx, a.UserID); err != nil {
			return nil, err
		}
	}
	versions := map[string]int{}
	if f := d.hooks.secretVersions; f != nil && len(st.Secrets) > 0 {
		if versions, err = f(ctx, a.UserID); err != nil {
			return nil, err
		}
	}
	for _, name := range st.Secrets {
		ds := httpapi.DesiredSecret{Name: name, Configured: configured[name]}
		if v, ok := versions[name]; ok && configured[name] {
			ds.Version = &v
		}
		doc.Secrets = append(doc.Secrets, ds)
	}
	etag, err := desiredETag(doc)
	if err != nil {
		return nil, err
	}
	if req.Params.IfNoneMatch != nil && *req.Params.IfNoneMatch == etag {
		return httpapi.GetDesiredState304Response{Headers: httpapi.GetDesiredState304ResponseHeaders{ETag: &etag}}, nil
	}
	return httpapi.GetDesiredState200JSONResponse{Body: doc, Headers: httpapi.GetDesiredState200ResponseHeaders{ETag: &etag}}, nil
}

func (d *deliveryAPI) GetBundle(ctx context.Context, req httpapi.GetBundleRequestObject) (httpapi.GetBundleResponseObject, error) {
	a, err := deliveryCaller(ctx)
	if err != nil {
		return nil, err
	}
	data, err := d.svc.Bundle(ctx, a, req.Digest)
	if err != nil {
		return nil, deliveryError(ctx, err)
	}
	etag := `"` + req.Digest + `"`
	return httpapi.GetBundle200ApplicationxTarResponse{Body: bytes.NewReader(data), ContentLength: int64(len(data)),
		Headers: httpapi.GetBundle200ResponseHeaders{ETag: &etag}}, nil
}

func (d *deliveryAPI) ReportSync(ctx context.Context, req httpapi.ReportSyncRequestObject) (httpapi.ReportSyncResponseObject, error) {
	a, err := deliveryCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem("body", "required")
	}
	b := req.Body
	r := delivery.SyncReport{CLIVersion: b.CliVersion, OS: string(b.Os), Arch: b.Arch, FinishedAt: b.FinishedAt, Result: string(b.Result)}
	for _, t := range b.Tools {
		dt := delivery.DetectedTool{ID: t.Tool}
		if t.Version != nil {
			dt.Version = *t.Version
		}
		r.Tools = append(r.Tools, dt)
	}
	for _, in := range b.Installs {
		ir := delivery.InstallReport{Code: in.Code, Version: in.Version, Tool: in.Tool, Status: string(in.Status)}
		if in.Degraded != nil {
			ir.Degraded = *in.Degraded
		}
		if in.Form != nil {
			ir.Form = *in.Form
		}
		if in.Reason != nil {
			ir.Reason = *in.Reason
		}
		r.Installs = append(r.Installs, ir)
	}
	if err := d.svc.Report(ctx, a, r); err != nil {
		return nil, deliveryError(ctx, err)
	}
	return httpapi.ReportSync204Response{}, nil
}

func (d *deliveryAPI) GetInstallsSummary(ctx context.Context, req httpapi.GetInstallsSummaryRequestObject) (httpapi.GetInstallsSummaryResponseObject, error) {
	a, err := deliveryCaller(ctx)
	if err != nil {
		return nil, err
	}
	product := ""
	if req.Params.Product != nil {
		product = *req.Params.Product
	}
	u, err := d.svc.Summary(ctx, a, product)
	if err != nil {
		return nil, deliveryError(ctx, err)
	}
	out := httpapi.GetInstallsSummary200JSONResponse{
		PeopleConnected: u.People, DevicesActive: u.Devices,
		ByTool: []httpapi.ToolCount{}, CliVersions: []httpapi.VersionCount{},
	}
	out.RequiredComplete.People, out.RequiredComplete.Of = u.RequiredComplete, u.People
	for _, c := range delivery.Counts(u.ByTool) {
		out.ByTool = append(out.ByTool, httpapi.ToolCount{Tool: c.Key, People: c.N})
	}
	for _, c := range delivery.Counts(u.CLIVersions) {
		out.CliVersions = append(out.CliVersions, httpapi.VersionCount{Version: c.Key, People: c.N})
	}
	return out, nil
}

// ---------------------------------------------------------------- hooks

// deliveryHooks give the other pages what delivery knows: the catalog pages (hooks.delivery),
// the tool adapters page (hooks.toolUsage) and my devices (authnapi.Options.DeviceSyncs).
type deliveryHooks struct {
	svc   *delivery.Service
	names auditNames
}

var (
	_ catalogDelivery = deliveryHooks{}
	_ toolUsage       = deliveryHooks{}
)

func (h deliveryHooks) Preferences(ctx context.Context, userID string) (map[string]bool, error) {
	return h.svc.Preferences(ctx, userID)
}

func (h deliveryHooks) InstalledPeople(ctx context.Context) (map[string]int, error) {
	return h.svc.InstalledPeople(ctx)
}

func (h deliveryHooks) Installed(ctx context.Context, resourceID string) (map[string]int, error) {
	return h.svc.Installed(ctx, resourceID)
}

func (h deliveryHooks) MyInstalls(ctx context.Context, userID, resourceID string) ([]httpapi.Install, error) {
	ins, err := h.svc.MyInstalls(ctx, userID, resourceID)
	if err != nil {
		return nil, err
	}
	out := make([]httpapi.Install, 0, len(ins))
	for _, in := range ins {
		name := in.DeviceID
		if h.names != nil {
			if n, ok := h.names.Device(ctx, in.DeviceID); ok {
				name = n
			}
		}
		out = append(out, httpapi.Install{DeviceId: in.DeviceID, DeviceName: name, Tool: in.Tool, Version: in.Version,
			Status: httpapi.InstallStatus(in.Status), Degraded: in.Degraded, Form: optStr(in.Form)})
	}
	return out, nil
}

func (h deliveryHooks) PeopleByTool(ctx context.Context) (map[string]int, error) {
	u, err := h.svc.UsageOf(ctx, nil)
	if err != nil {
		return nil, err
	}
	return u.ByTool, nil
}

func (h deliveryHooks) CLIVersions(ctx context.Context) ([]httpapi.VersionCount, error) {
	u, err := h.svc.UsageOf(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := []httpapi.VersionCount{}
	for _, c := range delivery.Counts(u.CLIVersions) {
		out = append(out, httpapi.VersionCount{Version: c.Key, People: c.N})
	}
	return out, nil
}

// DeviceSyncs is authnapi.Options.DeviceSyncs.
func (h deliveryHooks) DeviceSyncs(ctx context.Context, ids []string) (map[string]authnapi.DeviceSync, error) {
	syncs, err := h.svc.DeviceSyncs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]authnapi.DeviceSync, len(syncs))
	for id, s := range syncs {
		ds := authnapi.DeviceSync{LastSync: httpapi.LastSync{At: s.At, Result: httpapi.SyncResult(s.Result)}, Tools: []httpapi.DetectedTool{}}
		for _, t := range s.Tools {
			ds.Tools = append(ds.Tools, httpapi.DetectedTool{Tool: t.ID, Version: optStr(t.Version)})
		}
		out[id] = ds
	}
	return out, nil
}
