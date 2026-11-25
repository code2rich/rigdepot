// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/catalog"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

// catalogAPI implements the operations of the published catalog (tag catalog, except the
// personal preference, which is delivery's) and of resource management (tag resources):
// drafts, uploads, check results and waivers, publishing, versions, rollback, disabling,
// impact. The rules are in package catalog.
type catalogAPI struct {
	cat   *catalog.Service
	hooks *hooks
	// texts renders the findings' messages (locales/*/check.json); defaultLocale is used when
	// the request names no language the texts have.
	texts         *i18n.Catalog
	defaultLocale string
}

// catalogDelivery is what the catalog pages show from package delivery (M1-27): what people
// turned on or off and where resources are installed. Nil until delivery exists: everything
// is then on by its policy and installed nowhere.
type catalogDelivery interface {
	// Preferences returns, by resource id, what the person turned on (true) or off (false).
	Preferences(ctx context.Context, userID string) (map[string]bool, error)
	// InstalledPeople returns, by resource id, how many people have it on at least one computer.
	InstalledPeople(ctx context.Context) (map[string]int, error)
	// Installed returns, for one resource and by user id, on how many computers each person has it.
	Installed(ctx context.Context, resourceID string) (map[string]int, error)
	// MyInstalls returns where a resource is on the person's computers, per tool.
	MyInstalls(ctx context.Context, userID, resourceID string) ([]httpapi.Install, error)
}

// catalogCaller returns the caller as access knows it, or errSignInRequired.
func catalogCaller(ctx context.Context) (access.Actor, error) {
	a, ok := access.ActorFrom(ctx)
	if !ok || a.UserID == "" {
		return access.Actor{}, errSignInRequired
	}
	return a, nil
}

// catalogDetails are the English texts of the catalog's problems.
// TODO(i18n): render them in the request's language once locales/*/server.json has them.
var catalogDetails = map[string]string{
	"access.denied":             "You are not allowed to do this.",
	"object.not_found":          "Not found, or not visible to you.",
	"request.invalid":           "The request has invalid fields.",
	"precondition.failed":       "This changed after you opened it. Reload to see the latest.",
	"precondition.required":     "Send the ETag you are basing this change on in If-Match.",
	"resource.check_blocked":    "Problems that must be fixed block publishing.",
	"resource.no_draft":         "There is no draft to publish or discard.",
	"resource.from_repository":  "This resource comes from a repository. Change it there.",
	"resource.kind_mismatch":    "The resource is of another kind.",
	"resource.code_taken":       "Another resource already has this code.",
	"resource.plaintext_secret": "The definition holds a secret in plain text. Refer to it as ${secret:NAME} instead.",
	"check.not_waivable":        "Only suspected secrets can be marked as false positives.",
	"bundle.unsafe_path":        "The archive breaks a path safety rule.",
	"bundle.unknown_format":     "Upload a zip, tar or tar.gz archive.",
	"request.too_large":         "The upload is over the size limit.",
}

var catalogStatus = map[catalog.Kind]int{
	catalog.KindInvalid:              http.StatusBadRequest,
	catalog.KindDenied:               http.StatusForbidden,
	catalog.KindNotFound:             http.StatusNotFound,
	catalog.KindConflict:             http.StatusConflict,
	catalog.KindPreconditionFailed:   http.StatusPreconditionFailed,
	catalog.KindPreconditionRequired: http.StatusPreconditionRequired,
	catalog.KindTooLarge:             http.StatusRequestEntityTooLarge,
	catalog.KindUnsupported:          http.StatusUnsupportedMediaType,
}

// catalogError turns the catalog's errors into problem documents; other errors pass on as
// faults (500).
func catalogError(ctx context.Context, err error) error {
	var e *catalog.Error
	if !errors.As(err, &e) {
		return err
	}
	status, ok := catalogStatus[e.Kind]
	if !ok {
		return err
	}
	p := httpapi.NewProblem(status, e.Code, catalogDetails[e.Code])
	if len(e.Params) > 0 {
		params := e.Params
		p.Problem.Params = &params
	}
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

// printer renders messages in the request's language: Accept-Language, then the server's
// default. TODO(M1-20): the signed-in person's own setting first.
func (c *catalogAPI) printer(ctx context.Context) *i18n.Printer {
	prefs := acceptLanguages(ctx)
	return c.texts.Printer(c.texts.Match(append(prefs, c.defaultLocale)...))
}

// ---------------------------------------------------------------- conversions

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func strs(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func (c *catalogAPI) userRef(ctx context.Context, id string) httpapi.UserRef {
	ref := httpapi.UserRef{Id: id, Username: id, DisplayName: id}
	if c.hooks.names != nil {
		if u, d, ok := c.hooks.names.User(ctx, id); ok {
			ref.Username, ref.DisplayName = u, d
		}
	}
	return ref
}

func (c *catalogAPI) userRefs(ctx context.Context, ids []string) []httpapi.UserRef {
	out := make([]httpapi.UserRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, c.userRef(ctx, id))
	}
	return out
}

func overridable(fs []string) []httpapi.OverridableField {
	out := make([]httpapi.OverridableField, 0, len(fs))
	for _, f := range fs {
		out = append(out, httpapi.OverridableField(f))
	}
	return out
}

func mcpDelivery(d catalog.Delivery) *httpapi.Delivery {
	if d == "" {
		return nil
	}
	v := httpapi.Delivery(d)
	return &v
}

func (c *catalogAPI) refs(ctx context.Context, codes []string) []httpapi.ResourceRef {
	rs, _ := c.cat.Refs(ctx, codes)
	return refsOut(rs)
}

func refsOut(rs []catalog.Ref) []httpapi.ResourceRef {
	out := make([]httpapi.ResourceRef, 0, len(rs))
	for _, r := range rs {
		out = append(out, httpapi.ResourceRef{Code: r.Code, Kind: r.Kind, Name: r.Name})
	}
	return out
}

func counts(n catalog.Counts) httpapi.CheckCounts {
	return httpapi.CheckCounts{Blocking: n.Blocking, Advisory: n.Advisory}
}

func (c *catalogAPI) managed(ctx context.Context, r catalog.Resource) httpapi.ManagedResource {
	out := httpapi.ManagedResource{
		Code: r.Code, Kind: r.Kind, Name: r.Name, Summary: optStr(r.Summary), Stages: strs(r.Stages),
		Audience: httpapi.Audience(r.Audience), Scope: string(r.Scope), Policy: httpapi.Policy(r.Policy),
		Origin: httpapi.Origin(r.Origin), Status: httpapi.ResourceStatus(r.Status), HasDraft: r.HasDraft,
		Checks: counts(r.Checks), UpdatedAt: r.UpdatedAt, When: optStr(r.Meta.When), Try: optStr(r.Meta.Try),
		Delivery: mcpDelivery(r.Meta.Delivery), Maintainers: c.userRefs(ctx, r.Maintainers),
		Overrides: overridable(r.Overrides), Secrets: strs(r.Meta.Secrets), References: c.refs(ctx, r.References),
		DisabledReason: optStr(r.DisabledReason), CreatedAt: r.CreatedAt, Etag: r.ETag(),
	}
	if r.RepositoryDiffers != nil {
		d := overridable(r.RepositoryDiffers)
		out.RepositoryDiffers = &d
	}
	if r.Version > 0 {
		out.Version = &r.Version
		at := r.PublishedAt
		out.PublishedAt = &at
	}
	if r.Origin == catalog.OriginRepo && r.SourceID != "" {
		commit := r.DraftCommit
		if commit == "" {
			commit = r.LiveCommit
		}
		out.Source = &httpapi.ResourceSource{Id: r.SourceID, Name: c.cat.SourceName(ctx, r.SourceID), Path: r.SourcePath, Commit: optStr(commit)}
	}
	return out
}

func summary(r catalog.Resource) httpapi.ResourceSummary {
	out := httpapi.ResourceSummary{
		Code: r.Code, Kind: r.Kind, Name: r.Name, Summary: optStr(r.Summary), Stages: strs(r.Stages),
		Audience: httpapi.Audience(r.Audience), Scope: string(r.Scope), Policy: httpapi.Policy(r.Policy),
		Origin: httpapi.Origin(r.Origin), Status: httpapi.ResourceStatus(r.Status), HasDraft: r.HasDraft,
		Checks: counts(r.Checks), UpdatedAt: r.UpdatedAt,
	}
	if r.Version > 0 {
		out.Version = &r.Version
	}
	return out
}

func metaOut(m catalog.Meta) httpapi.ResourceMeta {
	out := httpapi.ResourceMeta{Name: m.Name, Summary: optStr(m.Summary), When: optStr(m.When), Try: optStr(m.Try)}
	if m.Audience != "" {
		a := httpapi.Audience(m.Audience)
		out.Audience = &a
	}
	if m.Scope != "" {
		s := string(m.Scope)
		out.Scope = &s
	}
	if m.Policy != "" {
		p := httpapi.Policy(m.Policy)
		out.Policy = &p
	}
	list := func(xs []string) *[]string {
		if len(xs) == 0 {
			return nil
		}
		return &xs
	}
	out.Stages, out.Secrets, out.Maintainers, out.Tags = list(m.Stages), list(m.Secrets), list(m.Maintainers), list(m.Tags)
	if len(m.I18n) > 0 {
		t := map[string]httpapi.ResourceMetaText{}
		for lang, x := range m.I18n {
			t[lang] = httpapi.ResourceMetaText{Name: optStr(x.Name), Summary: optStr(x.Summary), When: optStr(x.When), Try: optStr(x.Try)}
		}
		out.I18n = &t
	}
	return out
}

func metaIn(m httpapi.ResourceMeta) resource.Meta {
	out := resource.Meta{Name: m.Name}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	out.Summary, out.When, out.Try = deref(m.Summary), deref(m.When), deref(m.Try)
	if m.Audience != nil {
		out.Audience = resource.Audience(*m.Audience)
	}
	if m.Scope != nil {
		out.Scope = resource.Scope(*m.Scope)
	}
	if m.Policy != nil {
		out.Policy = resource.Policy(*m.Policy)
	}
	list := func(p *[]string) []string {
		if p == nil {
			return nil
		}
		return *p
	}
	out.Stages, out.Secrets, out.Maintainers, out.Tags = list(m.Stages), list(m.Secrets), list(m.Maintainers), list(m.Tags)
	if m.I18n != nil {
		out.I18n = map[string]resource.Text{}
		for lang, x := range *m.I18n {
			out.I18n[lang] = resource.Text{Name: deref(x.Name), Summary: deref(x.Summary), When: deref(x.When), Try: deref(x.Try)}
		}
	}
	return out
}

func (c *catalogAPI) findings(ctx context.Context, rep catalog.Report) []httpapi.Finding {
	p := c.printer(ctx)
	out := make([]httpapi.Finding, 0, len(rep.Findings))
	for _, f := range rep.Findings {
		args := make([]i18n.Arg, 0, len(f.Params))
		for k, v := range f.Params {
			args = append(args, i18n.V(k, v))
		}
		x := httpapi.Finding{Id: f.ID, Rule: f.Rule, Severity: httpapi.Severity(f.Severity.String()), Message: p.T(f.Message, args...), File: optStr(f.File)}
		if f.Line > 0 {
			x.Line = &f.Line
		}
		if f.FirstSeen > 0 {
			t := time.UnixMilli(f.FirstSeen).UTC()
			x.FirstSeenAt = &t
		}
		if f.Waiver != nil {
			w := c.waiver(ctx, *f.Waiver)
			x.Waiver = &w
		}
		out = append(out, x)
	}
	return out
}

func (c *catalogAPI) waiver(ctx context.Context, w catalog.Waiver) httpapi.Waiver {
	return httpapi.Waiver{Id: w.ID, Rule: w.Rule, Reason: w.Reason, By: c.userRef(ctx, w.By), At: w.At}
}

func filesOut(fs []catalog.File) []httpapi.ResourceFile {
	out := make([]httpapi.ResourceFile, 0, len(fs))
	for _, f := range fs {
		out = append(out, httpapi.ResourceFile{Path: f.Path, Size: f.Size})
	}
	return out
}

func (c *catalogAPI) draft(ctx context.Context, d catalog.Draft) (httpapi.Draft, error) {
	n := d.Checks.Counts()
	out := httpapi.Draft{
		Code: d.Code, Kind: d.Kind, Digest: string(d.Digest), Meta: metaOut(d.Meta), Files: filesOut(d.Files),
		Checks:       httpapi.CheckReport{Ruleset: d.Checks.Ruleset, Blocking: n.Blocking, Advisory: n.Advisory, Findings: c.findings(ctx, d.Checks)},
		SourceCommit: optStr(d.Commit), UpdatedAt: d.UpdatedAt, Etag: d.ETag(),
	}
	if d.BaseVersion > 0 {
		out.BaseVersion = &d.BaseVersion
	}
	if d.UpdatedBy != "" {
		ref := c.userRef(ctx, d.UpdatedBy)
		out.UpdatedBy = &ref
	}
	def, _, err := c.cat.Definition(ctx, d.Kind, d.Code, d.Bundle)
	if err != nil {
		return out, err
	}
	out.Definition = def
	return out, nil
}

func (c *catalogAPI) version(ctx context.Context, v catalog.Version, single bool) httpapi.ResourceVersion {
	out := httpapi.ResourceVersion{
		Number: v.Number, Digest: string(v.Digest), Bundle: v.Bundle, Note: optStr(v.Note), SourceCommit: optStr(v.Commit),
		Ruleset: v.Checks.Ruleset, Checks: counts(v.Checks.Counts()), PublishedBy: c.userRef(ctx, v.PublishedBy), PublishedAt: v.PublishedAt,
	}
	if v.RollbackOf > 0 {
		out.RollbackOf = &v.RollbackOf
	}
	if single {
		fs := c.findings(ctx, v.Checks)
		m := metaOut(v.Meta)
		out.Findings, out.Meta = &fs, &m
	}
	return out
}

// ---------------------------------------------------------------- resources

func (c *catalogAPI) ListResources(ctx context.Context, req httpapi.ListResourcesRequestObject) (httpapi.ListResourcesResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	q := req.Params
	f := catalog.Filter{Pending: q.Pending, Blocked: q.Blocked}
	if q.Kind != nil {
		f.Kind = *q.Kind
	}
	if q.Status != nil {
		f.Status = catalog.Status(*q.Status)
	}
	if q.Scope != nil {
		f.Scope = resource.Scope(*q.Scope)
	}
	if q.Source != nil {
		f.SourceID = *q.Source
	}
	if q.Q != nil {
		f.Query = *q.Q
	}
	rs, err := c.cat.List(ctx, a, f)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	out := httpapi.ListResources200JSONResponse{Items: make([]httpapi.ResourceSummary, 0, len(rs))}
	for _, r := range rs {
		out.Items = append(out.Items, summary(r))
	}
	return out, nil
}

func (c *catalogAPI) GetResource(ctx context.Context, req httpapi.GetResourceRequestObject) (httpapi.GetResourceResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	r, err := c.cat.Get(ctx, a, req.Code)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	etag := r.ETag()
	return httpapi.GetResource200JSONResponse{Body: c.managed(ctx, r), Headers: httpapi.GetResource200ResponseHeaders{ETag: &etag}}, nil
}

func (c *catalogAPI) UpdateResource(ctx context.Context, req httpapi.UpdateResourceRequestObject) (httpapi.UpdateResourceResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem("body", "required")
	}
	b := req.Body
	ch := catalog.Change{Name: b.Name, Summary: b.Summary, Stages: b.Stages, When: b.When, Try: b.Try, Maintainers: b.Maintainers}
	if b.Audience != nil {
		v := resource.Audience(*b.Audience)
		ch.Audience = &v
	}
	if b.Scope != nil {
		v := resource.Scope(*b.Scope)
		ch.Scope = &v
	}
	if b.Policy != nil {
		v := resource.Policy(*b.Policy)
		ch.Policy = &v
	}
	if b.Delivery != nil {
		v := catalog.Delivery(*b.Delivery)
		ch.Delivery = &v
	}
	if b.Reset != nil {
		for _, f := range *b.Reset {
			ch.Reset = append(ch.Reset, string(f))
		}
	}
	r, err := c.cat.Configure(ctx, a, req.Code, req.Params.IfMatch, ch)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	etag := r.ETag()
	return httpapi.UpdateResource200JSONResponse{Body: c.managed(ctx, r), Headers: httpapi.UpdateResource200ResponseHeaders{ETag: &etag}}, nil
}

func (c *catalogAPI) GetDraft(ctx context.Context, req httpapi.GetDraftRequestObject) (httpapi.GetDraftResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	d, err := c.cat.Draft(ctx, a, req.Code)
	if errors.Is(err, catalog.ErrNoDraft) {
		err = catalog.ErrNotFound
	}
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	body, err := c.draft(ctx, d)
	if err != nil {
		return nil, err
	}
	etag := d.ETag()
	return httpapi.GetDraft200JSONResponse{Body: body, Headers: httpapi.GetDraft200ResponseHeaders{ETag: &etag}}, nil
}

func (c *catalogAPI) PutDraft(ctx context.Context, req httpapi.PutDraftRequestObject) (httpapi.PutDraftResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem("body", "required")
	}
	def := map[string]any(req.Body.Definition)
	res, err := c.cat.PutDefinition(ctx, a, req.Code, req.Body.Kind, metaIn(req.Body.Meta), def, req.Params.IfMatch)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	body, err := c.draft(ctx, res.Draft)
	if err != nil {
		return nil, err
	}
	etag := res.Draft.ETag()
	if res.Created {
		return httpapi.PutDraft201JSONResponse{Body: body, Headers: httpapi.PutDraft201ResponseHeaders{ETag: &etag}}, nil
	}
	return httpapi.PutDraft200JSONResponse{Body: body, Headers: httpapi.PutDraft200ResponseHeaders{ETag: &etag}}, nil
}

func (c *catalogAPI) UploadDraftBundle(ctx context.Context, req httpapi.UploadDraftBundleRequestObject) (httpapi.UploadDraftBundleResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	// The body is bounded before it is read (limitUploads); a body without a length that
	// turns out larger stops here.
	data, err := io.ReadAll(req.Body)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, catalogError(ctx, catalog.ErrTooLarge)
	}
	if err != nil {
		return nil, err
	}
	kind := ""
	if req.Params.Kind != nil {
		kind = *req.Params.Kind
	}
	res, err := c.cat.Upload(ctx, a, req.Code, kind, data, req.Params.IfMatch)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	body, err := c.draft(ctx, res.Draft)
	if err != nil {
		return nil, err
	}
	etag := res.Draft.ETag()
	if res.Created {
		return httpapi.UploadDraftBundle201JSONResponse{Body: body, Headers: httpapi.UploadDraftBundle201ResponseHeaders{ETag: &etag}}, nil
	}
	return httpapi.UploadDraftBundle200JSONResponse{Body: body, Headers: httpapi.UploadDraftBundle200ResponseHeaders{ETag: &etag}}, nil
}

func (c *catalogAPI) DiscardDraft(ctx context.Context, req httpapi.DiscardDraftRequestObject) (httpapi.DiscardDraftResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.cat.DiscardDraft(ctx, a, req.Code, req.Params.IfMatch); err != nil {
		return nil, catalogError(ctx, err)
	}
	return httpapi.DiscardDraft204Response{}, nil
}

func (c *catalogAPI) GetDraftFile(ctx context.Context, req httpapi.GetDraftFileRequestObject) (httpapi.GetDraftFileResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	data, err := c.cat.DraftFile(ctx, a, req.Code, req.Params.Path)
	if errors.Is(err, catalog.ErrNoDraft) {
		err = catalog.ErrNotFound
	}
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	return httpapi.GetDraftFile200ApplicationoctetStreamResponse{Body: bytes.NewReader(data), ContentLength: int64(len(data))}, nil
}

func (c *catalogAPI) GetDraftDiff(ctx context.Context, req httpapi.GetDraftDiffRequestObject) (httpapi.GetDraftDiffResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	base, changes, err := c.cat.DraftDiff(ctx, a, req.Code)
	if errors.Is(err, catalog.ErrNoDraft) {
		err = catalog.ErrNotFound
	}
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	out := httpapi.GetDraftDiff200JSONResponse{Files: make([]httpapi.FileChange, 0, len(changes))}
	if base > 0 {
		out.BaseVersion = &base
	}
	for _, ch := range changes {
		out.Files = append(out.Files, httpapi.FileChange{Path: ch.Path, Change: httpapi.FileChangeChange(ch.Change), Unified: optStr(ch.Unified)})
	}
	return out, nil
}

func (c *catalogAPI) PublishResource(ctx context.Context, req httpapi.PublishResourceRequestObject) (httpapi.PublishResourceResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	note := ""
	if req.Body != nil && req.Body.Note != nil {
		note = *req.Body.Note
	}
	v, r, err := c.cat.Publish(ctx, a, req.Code, req.Params.IfMatch, note)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	etag := r.ETag()
	return httpapi.PublishResource201JSONResponse{Body: c.version(ctx, v, true), Headers: httpapi.PublishResource201ResponseHeaders{ETag: &etag}}, nil
}

func (c *catalogAPI) ListResourceVersions(ctx context.Context, req httpapi.ListResourceVersionsRequestObject) (httpapi.ListResourceVersionsResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	limit, cursor := 0, ""
	if req.Params.Limit != nil {
		if *req.Params.Limit < 1 || *req.Params.Limit > 200 {
			return nil, fieldProblem("limit", "invalid")
		}
		limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		cursor = *req.Params.Cursor
	}
	vs, next, err := c.cat.Versions(ctx, a, req.Code, limit, cursor)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	out := httpapi.ListResourceVersions200JSONResponse{Items: make([]httpapi.ResourceVersion, 0, len(vs)), NextCursor: optStr(next)}
	for _, v := range vs {
		out.Items = append(out.Items, c.version(ctx, v, false))
	}
	return out, nil
}

func (c *catalogAPI) GetResourceVersion(ctx context.Context, req httpapi.GetResourceVersionRequestObject) (httpapi.GetResourceVersionResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	v, err := c.cat.Version(ctx, a, req.Code, req.Number)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	return httpapi.GetResourceVersion200JSONResponse(c.version(ctx, v, true)), nil
}

func (c *catalogAPI) RollbackResource(ctx context.Context, req httpapi.RollbackResourceRequestObject) (httpapi.RollbackResourceResponseObject, error) {
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
	v, r, err := c.cat.Rollback(ctx, a, req.Code, req.Params.IfMatch, req.Body.ToVersion, note)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	etag := r.ETag()
	return httpapi.RollbackResource201JSONResponse{Body: c.version(ctx, v, true), Headers: httpapi.RollbackResource201ResponseHeaders{ETag: &etag}}, nil
}

func (c *catalogAPI) GetResourceImpact(ctx context.Context, req httpapi.GetResourceImpactRequestObject) (httpapi.GetResourceImpactResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	r, err := c.cat.Get(ctx, a, req.Code)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	installed := map[string]int{}
	if c.hooks.delivery != nil {
		if installed, err = c.hooks.delivery.Installed(ctx, r.ID); err != nil {
			return nil, err
		}
	}
	var scope *resource.Scope
	if req.Params.Scope != nil {
		s := resource.Scope(*req.Params.Scope)
		scope = &s
	}
	im, err := c.cat.Impact(ctx, a, req.Code, scope, installed)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	return httpapi.GetResourceImpact200JSONResponse{People: im.People, Devices: im.Devices, ReferencedBy: refsOut(im.ReferencedBy)}, nil
}

func (c *catalogAPI) DisableResource(ctx context.Context, req httpapi.DisableResourceRequestObject) (httpapi.DisableResourceResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem("body", "required")
	}
	r, err := c.cat.Disable(ctx, a, req.Code, req.Params.IfMatch, req.Body.Reason)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	etag := r.ETag()
	return httpapi.DisableResource200JSONResponse{Body: c.managed(ctx, r), Headers: httpapi.DisableResource200ResponseHeaders{ETag: &etag}}, nil
}

func (c *catalogAPI) EnableResource(ctx context.Context, req httpapi.EnableResourceRequestObject) (httpapi.EnableResourceResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	r, err := c.cat.Enable(ctx, a, req.Code, req.Params.IfMatch)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	etag := r.ETag()
	return httpapi.EnableResource200JSONResponse{Body: c.managed(ctx, r), Headers: httpapi.EnableResource200ResponseHeaders{ETag: &etag}}, nil
}

func (c *catalogAPI) WaiveFinding(ctx context.Context, req httpapi.WaiveFindingRequestObject) (httpapi.WaiveFindingResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem("body", "required")
	}
	w, err := c.cat.Waive(ctx, a, req.Code, req.Body.Finding, strings.TrimSpace(req.Body.Reason))
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	return httpapi.WaiveFinding201JSONResponse(c.waiver(ctx, w)), nil
}

func (c *catalogAPI) ListCheckRules(ctx context.Context, _ httpapi.ListCheckRulesRequestObject) (httpapi.ListCheckRulesResponseObject, error) {
	if _, err := catalogCaller(ctx); err != nil {
		return nil, err
	}
	version, rules := c.cat.Rules()
	out := httpapi.ListCheckRules200JSONResponse{Version: version, Rules: make([]httpapi.CheckRule, 0, len(rules))}
	for _, r := range rules {
		// TODO: translated names and descriptions need a way for a rule to name itself
		// (ext/check); until then the rule's ID stands in.
		rule := httpapi.CheckRule{Id: r.ID, Name: r.ID, Description: "", Severity: severity(r.Severity)}
		if r.Waivable {
			rule.Waivable = &r.Waivable
		}
		out.Rules = append(out.Rules, rule)
	}
	return out, nil
}

// stageNames are the translations of the default stages' names (locales/*/server.json).
var stageNames = map[string]string{
	"requirement":  msg.StageRequirement,
	"architecture": msg.StageArchitecture,
	"design":       msg.StageDesign,
	"develop":      msg.StageDevelop,
	"test":         msg.StageTest,
	"scm":          msg.StageScm,
	"release":      msg.StageRelease,
	"ops":          msg.StageOps,
}

// ListStages lists the stages of the delivery process in the request's language. Anyone signed
// in may: a person whose account is not active yet picks the stages they follow when asking to
// join.
func (c *catalogAPI) ListStages(ctx context.Context, _ httpapi.ListStagesRequestObject) (httpapi.ListStagesResponseObject, error) {
	if _, err := catalogCaller(ctx); err != nil {
		return nil, err
	}
	stages, err := c.cat.Stages(ctx)
	if err != nil {
		return nil, err
	}
	p := c.printer(ctx)
	out := httpapi.ListStages200JSONResponse{Items: make([]httpapi.Stage, 0, len(stages))}
	for _, st := range stages {
		name := st.Name
		if key, ok := stageNames[st.ID]; ok && name == "" {
			name = p.T(key)
		}
		if name == "" {
			name = st.ID
		}
		out.Items = append(out.Items, httpapi.Stage{Id: st.ID, Name: name, Hidden: st.Hidden})
	}
	return out, nil
}

func severity(s extcheck.Severity) httpapi.Severity { return httpapi.Severity(s.String()) }

// ---------------------------------------------------------------- catalog

// enabled is whether a resource is on for someone: required always, recommended unless they
// turned it off, optional only when they turned it on.
func enabled(r catalog.Resource, prefs map[string]bool) bool {
	pref, chose := prefs[r.ID]
	switch r.Policy {
	case resource.PolicyRequired:
		return true
	case resource.PolicyOptional:
		return chose && pref
	}
	return !chose || pref
}

// personal is what the catalog pages need about the caller from other domains.
type personal struct {
	prefs      map[string]bool
	installed  map[string]int
	configured map[string]bool
}

func (c *catalogAPI) personal(ctx context.Context, userID string) (personal, error) {
	p := personal{prefs: map[string]bool{}, installed: map[string]int{}, configured: map[string]bool{}}
	var err error
	if d := c.hooks.delivery; d != nil {
		if p.prefs, err = d.Preferences(ctx, userID); err != nil {
			return p, err
		}
		if p.installed, err = d.InstalledPeople(ctx); err != nil {
			return p, err
		}
	}
	if f := c.hooks.secretsConfigured; f != nil {
		if p.configured, err = f(ctx, userID); err != nil {
			return p, err
		}
	}
	return p, nil
}

func (p personal) secrets(names []string) *[]httpapi.SecretNeed {
	if len(names) == 0 {
		return nil
	}
	out := make([]httpapi.SecretNeed, 0, len(names))
	for _, n := range names {
		out = append(out, httpapi.SecretNeed{Name: n, Configured: p.configured[n]})
	}
	return &out
}

func (c *catalogAPI) ListCatalog(ctx context.Context, req httpapi.ListCatalogRequestObject) (httpapi.ListCatalogResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	q := req.Params
	var f catalog.CatalogFilter
	if q.Kind != nil {
		f.Kind = *q.Kind
	}
	if q.Stage != nil {
		f.Stage = *q.Stage
	}
	if q.Audience != nil {
		f.Audience = resource.Audience(*q.Audience)
	}
	if q.Q != nil {
		f.Query = *q.Q
	}
	rs, err := c.cat.Catalog(ctx, a, f)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	p, err := c.personal(ctx, a.UserID)
	if err != nil {
		return nil, err
	}
	out := httpapi.ListCatalog200JSONResponse{Items: make([]httpapi.CatalogItem, 0, len(rs))}
	for _, r := range rs {
		out.Items = append(out.Items, httpapi.CatalogItem{
			Code: r.Code, Kind: r.Kind, Name: r.Name, Summary: optStr(r.Summary), Stages: strs(r.Stages),
			Audience: httpapi.Audience(r.Audience), Scope: string(r.Scope), Policy: httpapi.Policy(r.Policy),
			Origin: httpapi.Origin(r.Origin), Version: r.Version, Enabled: enabled(r, p.prefs),
			InstalledPeople: p.installed[r.ID], Secrets: p.secrets(r.Meta.Secrets), Delivery: mcpDelivery(r.Meta.Delivery),
		})
	}
	return out, nil
}

func (c *catalogAPI) GetCatalogResource(ctx context.Context, req httpapi.GetCatalogResourceRequestObject) (httpapi.GetCatalogResourceResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	r, v, err := c.cat.Published(ctx, a, req.Code)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	p, err := c.personal(ctx, a.UserID)
	if err != nil {
		return nil, err
	}
	files, err := c.cat.Files(ctx, v.Bundle)
	if err != nil {
		return nil, err
	}
	def, local, err := c.cat.Definition(ctx, r.Kind, r.Code, v.Bundle)
	if err != nil {
		return nil, err
	}
	referencedBy, err := c.cat.ReferencedBy(ctx, r.Code)
	if err != nil {
		return nil, err
	}
	out := httpapi.GetCatalogResource200JSONResponse{
		Code: r.Code, Kind: r.Kind, Name: r.Name, Summary: optStr(r.Summary), Stages: strs(r.Stages),
		Audience: httpapi.Audience(r.Audience), Scope: string(r.Scope), Policy: httpapi.Policy(r.Policy),
		Origin: httpapi.Origin(r.Origin), Version: r.Version, Enabled: enabled(r, p.prefs),
		InstalledPeople: p.installed[r.ID], Secrets: p.secrets(r.Meta.Secrets), Delivery: mcpDelivery(r.Meta.Delivery),
		When: optStr(r.Meta.When), Try: optStr(r.Meta.Try), Digest: string(v.Digest), PublishedAt: v.PublishedAt,
		Files: filesOut(files), Definition: def, References: c.refs(ctx, r.References),
		ReferencedBy: refsOut(referencedBy), MyInstalls: []httpapi.Install{},
	}
	if len(r.Maintainers) > 0 {
		m := c.userRefs(ctx, r.Maintainers)
		out.Maintainers = &m
	}
	if len(local) > 0 {
		items := make([]httpapi.LocalConfigItem, 0, len(local))
		for _, l := range local {
			items = append(items, httpapi.LocalConfigItem{Name: l.Name, Description: optStr(l.Description)})
		}
		out.LocalConfig = &items
	}
	if d := c.hooks.delivery; d != nil {
		mine, err := d.MyInstalls(ctx, a.UserID, r.ID)
		if err != nil {
			return nil, err
		}
		if mine != nil {
			out.MyInstalls = mine
		}
	}
	return out, nil
}

func (c *catalogAPI) GetCatalogFile(ctx context.Context, req httpapi.GetCatalogFileRequestObject) (httpapi.GetCatalogFileResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	version := 0
	if req.Params.Version != nil {
		version = *req.Params.Version
	}
	data, err := c.cat.PublishedFile(ctx, a, req.Code, version, req.Params.Path)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	return httpapi.GetCatalogFile200ApplicationoctetStreamResponse{Body: bytes.NewReader(data), ContentLength: int64(len(data))}, nil
}

// ---------------------------------------------------------------- request plumbing

type acceptLanguageKey struct{}

// withAcceptLanguage keeps the request's Accept-Language in its context for the operations
// that render text.
func withAcceptLanguage(r *http.Request) *http.Request {
	if v := r.Header.Get("Accept-Language"); v != "" {
		return r.WithContext(context.WithValue(r.Context(), acceptLanguageKey{}, v))
	}
	return r
}

// acceptLanguages returns the request's languages, most wanted first (quality values are not
// weighed: browsers list them in order).
func acceptLanguages(ctx context.Context) []string {
	v, _ := ctx.Value(acceptLanguageKey{}).(string)
	var out []string
	for _, part := range strings.Split(v, ",") {
		tag, _, _ := strings.Cut(part, ";")
		if tag = strings.TrimSpace(tag); tag != "" && tag != "*" {
			out = append(out, tag)
		}
	}
	return out
}

// isUpload reports whether the request uploads a resource's files.
func isUpload(r *http.Request) bool {
	return r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v1/resources/") && strings.HasSuffix(r.URL.Path, "/draft/bundle")
}

// limitUpload refuses an upload over limit bytes before reading it: by its Content-Length when
// it has one, otherwise by stopping the read at the limit. It reports whether the request may
// go on.
func limitUpload(w http.ResponseWriter, r *http.Request, limit int64) bool {
	if !isUpload(r) {
		return true
	}
	if r.ContentLength > limit {
		// Close the connection: the rest of the body is not read.
		w.Header().Set("Connection", "close")
		p := catalogError(r.Context(), catalog.ErrTooLarge).(*httpapi.ProblemError)
		p.Problem.Params = &map[string]any{"limit": limit}
		httpapi.WriteProblem(w, p.Problem)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return true
}
