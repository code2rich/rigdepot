// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import (
	"context"
	"strings"

	"github.com/code2rich/rigdepot/internal/server/access"
)

// Brief is a resource as another domain refers to it (feedback about a resource): who it is,
// its name, its live version and what a permission decision about it looks at. Reading a
// Brief checks no permission; the caller decides with Target.
type Brief struct {
	ID      string
	Code    string
	Kind    string
	Name    string
	Version int    // the live version; 0 before the first publish
	Product string // the product's id for a product-scoped resource
	Target  access.Target
}

func (r resourceRow) brief() Brief {
	return Brief{ID: r.ID, Code: r.Code, Kind: r.Kind, Name: r.Name, Version: r.Version, Product: r.ProductID, Target: r.target()}
}

// BriefByCode returns the resource with code; false when there is none.
func (s *Service) BriefByCode(ctx context.Context, code string) (Brief, bool, error) {
	r, found, err := s.findRow(ctx, s.db, code)
	if err != nil || !found {
		return Brief{}, false, err
	}
	return r.brief(), true, nil
}

// Briefs returns the resources with the given ids, by id. Ids of resources that do not exist
// are left out.
func (s *Service) Briefs(ctx context.Context, ids []string) (map[string]Brief, error) {
	out := map[string]Brief{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.loadRows(ctx, s.db, `r.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.brief()
	}
	return out, nil
}
