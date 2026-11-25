// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package delivery

import (
	"cmp"
	"context"
	"slices"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/catalog"
)

// Reason says why a resource is in a desired state. The values are the contract's.
type Reason string

const (
	// ReasonPolicy: required, or recommended and not turned off.
	ReasonPolicy Reason = "policy"
	// ReasonEnabled: optional, and the person turned it on.
	ReasonEnabled Reason = "enabled"
	// ReasonDependency: another delivered resource uses it.
	ReasonDependency Reason = "dependency"
)

// Item is one resource a person's computers should have.
type Item struct {
	// Resource is the resource with its metadata in force.
	Resource catalog.Resource
	// Live is the version to install.
	Live   catalog.Live
	Reason Reason
	// RequiredBy lists, for ReasonDependency, the codes of the delivered resources that use it,
	// sorted.
	RequiredBy []string
}

// State is what a person's computers should have.
type State struct {
	UserID string
	// Items are sorted by code.
	Items []Item
	// Secrets are the names of the personal secrets the items need, sorted.
	Secrets []string
}

// choose decides which of the resources a person sees they get, and why: by policy and their
// own choices (prefs, by resource id), then the dependencies of what they get. visible holds
// every resource the person sees, by code; a dependency outside it is not delivered.
func choose(visible map[string]catalog.Resource, prefs map[string]bool) map[string]*Item {
	out := map[string]*Item{}
	for code, r := range visible {
		pref, chose := prefs[r.ID]
		switch {
		case r.Policy == resource.PolicyRequired:
			out[code] = &Item{Resource: r, Reason: ReasonPolicy}
		case r.Policy == resource.PolicyOptional:
			if chose && pref {
				out[code] = &Item{Resource: r, Reason: ReasonEnabled}
			}
		case !chose || pref: // recommended, or a policy this program does not know
			out[code] = &Item{Resource: r, Reason: ReasonPolicy}
		}
	}
	// Dependencies, breadth first: what a dependency uses comes along too.
	queue := make([]string, 0, len(out))
	for code := range out {
		queue = append(queue, code)
	}
	slices.Sort(queue)
	for len(queue) > 0 {
		code := queue[0]
		queue = queue[1:]
		for _, ref := range out[code].Resource.References {
			dep, seen := visible[ref]
			if !seen || ref == code {
				continue
			}
			it, ok := out[ref]
			if !ok {
				it = &Item{Resource: dep, Reason: ReasonDependency}
				out[ref] = it
				queue = append(queue, ref)
			}
			if it.Reason == ReasonDependency && !slices.Contains(it.RequiredBy, code) {
				it.RequiredBy = append(it.RequiredBy, code)
			}
		}
	}
	return out
}

// Desired returns what the actor's computers should have. An account that is not active gets
// nothing.
func (s *Service) Desired(ctx context.Context, a access.Actor) (State, error) {
	st := State{UserID: a.UserID, Items: []Item{}, Secrets: []string{}}
	if a.UserID == "" {
		return st, ErrDenied
	}
	rs, err := s.catalog.Catalog(ctx, a, catalog.CatalogFilter{})
	if err != nil {
		return st, err
	}
	visible := make(map[string]catalog.Resource, len(rs))
	for _, r := range rs {
		visible[r.Code] = r
	}
	prefs, err := s.Preferences(ctx, a.UserID)
	if err != nil {
		return st, err
	}
	chosen := choose(visible, prefs)
	ids := make([]string, 0, len(chosen))
	for _, it := range chosen {
		ids = append(ids, it.Resource.ID)
	}
	live, err := s.catalog.LiveVersions(ctx, ids)
	if err != nil {
		return st, err
	}
	for _, it := range chosen {
		l, ok := live[it.Resource.ID]
		if !ok {
			// Disabled or unpublished between the two reads: the next sync sees it right.
			continue
		}
		it.Live = l
		slices.Sort(it.RequiredBy)
		st.Items = append(st.Items, *it)
		for _, name := range it.Resource.Meta.Secrets {
			if !slices.Contains(st.Secrets, name) {
				st.Secrets = append(st.Secrets, name)
			}
		}
	}
	slices.SortFunc(st.Items, func(x, y Item) int { return cmp.Compare(x.Resource.Code, y.Resource.Code) })
	slices.Sort(st.Secrets)
	return st, nil
}
