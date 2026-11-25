// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"

	"github.com/code2rich/rigdepot/internal/server/catalog"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

// sourcesAPI implements the operations of the resource repositories (tag sources). The rules,
// the sealing of the credential and the pull are in package catalog; the credential is
// write-only and no answer carries it.
type sourcesAPI struct {
	cat *catalog.Service
}

func sourceOut(s catalog.Source) httpapi.Source {
	out := httpapi.Source{Id: s.ID, Name: s.Name, Url: s.URL, Branch: s.Branch, Dir: s.Dir, Order: s.Order,
		HasCredential: s.HasCredential, ResourceCount: s.ResourceCount, Etag: s.ETag()}
	if p := s.LastPull; p != nil {
		lp := httpapi.SourcePull{At: p.At, Result: httpapi.SourcePullResult(p.Result), Error: optStr(p.Error), Commit: optStr(p.Commit)}
		if p.Commit != "" {
			imported, failed := p.Imported, p.Failed
			lp.Imported, lp.Failed = &imported, &failed
		}
		if !p.LastSuccessAt.IsZero() {
			at := p.LastSuccessAt
			lp.LastSuccessAt = &at
		}
		out.LastPull = &lp
	}
	return out
}

func sourceList(ss []catalog.Source) httpapi.SourceList {
	out := httpapi.SourceList{Items: make([]httpapi.Source, 0, len(ss))}
	for _, s := range ss {
		out.Items = append(out.Items, sourceOut(s))
	}
	return out
}

func (s *sourcesAPI) ListSources(ctx context.Context, _ httpapi.ListSourcesRequestObject) (httpapi.ListSourcesResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	ss, err := s.cat.Sources(ctx, a)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	return httpapi.ListSources200JSONResponse(sourceList(ss)), nil
}

func (s *sourcesAPI) CreateSource(ctx context.Context, req httpapi.CreateSourceRequestObject) (httpapi.CreateSourceResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem("body", "required")
	}
	b := req.Body
	in := catalog.SourceInput{Name: b.Name, URL: b.Url, Branch: b.Branch}
	if b.Dir != nil {
		in.Dir = *b.Dir
	}
	if b.Credential != nil {
		in.Credential = *b.Credential
	}
	src, err := s.cat.CreateSource(ctx, a, in)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	etag := src.ETag()
	return httpapi.CreateSource201JSONResponse{Body: sourceOut(src), Headers: httpapi.CreateSource201ResponseHeaders{ETag: &etag}}, nil
}

func (s *sourcesAPI) OrderSources(ctx context.Context, req httpapi.OrderSourcesRequestObject) (httpapi.OrderSourcesResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem("body", "required")
	}
	ss, err := s.cat.OrderSources(ctx, a, req.Body.Ids)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	return httpapi.OrderSources200JSONResponse(sourceList(ss)), nil
}

func (s *sourcesAPI) GetSource(ctx context.Context, req httpapi.GetSourceRequestObject) (httpapi.GetSourceResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	src, err := s.cat.Source(ctx, a, req.SourceId)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	etag := src.ETag()
	return httpapi.GetSource200JSONResponse{Body: sourceOut(src), Headers: httpapi.GetSource200ResponseHeaders{ETag: &etag}}, nil
}

func (s *sourcesAPI) UpdateSource(ctx context.Context, req httpapi.UpdateSourceRequestObject) (httpapi.UpdateSourceResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem("body", "required")
	}
	b := req.Body
	ch := catalog.SourceChange{Name: b.Name, URL: b.Url, Branch: b.Branch, Dir: b.Dir, Credential: b.Credential}
	if b.ClearCredential != nil {
		ch.ClearCredential = *b.ClearCredential
	}
	ifMatch := req.Params.IfMatch
	src, err := s.cat.UpdateSource(ctx, a, req.SourceId, &ifMatch, ch)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	etag := src.ETag()
	return httpapi.UpdateSource200JSONResponse{Body: sourceOut(src), Headers: httpapi.UpdateSource200ResponseHeaders{ETag: &etag}}, nil
}

func (s *sourcesAPI) DeleteSource(ctx context.Context, req httpapi.DeleteSourceRequestObject) (httpapi.DeleteSourceResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.cat.DeleteSource(ctx, a, req.SourceId); err != nil {
		return nil, catalogError(ctx, err)
	}
	return httpapi.DeleteSource204Response{}, nil
}

func (s *sourcesAPI) PullSource(ctx context.Context, req httpapi.PullSourceRequestObject) (httpapi.PullSourceResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	src, err := s.cat.RequestPull(ctx, a, req.SourceId)
	if err != nil {
		return nil, catalogError(ctx, err)
	}
	return httpapi.PullSource202JSONResponse(sourceOut(src)), nil
}
