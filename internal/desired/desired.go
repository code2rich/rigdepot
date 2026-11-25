// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package desired

import (
	"cmp"
	"slices"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/repoformat"
)

// Source says where a desired state came from. It is recorded in the manifest.
type Source struct {
	// Type is "repo" or "server".
	Type string
	// URL is the repository address or the server address.
	URL string
	// Ref and Dir are what was asked for in a repository; Commit is what Ref pointed at.
	Ref, Dir, Commit string
}

// The values of Source.Type.
const (
	SourceRepo   = "repo"
	SourceServer = "server"
)

// Resource is one resource that should be on this computer.
type Resource struct {
	// Ref names the resource. Version is 0 for a resource read from a repository.
	Ref resource.Ref
	// Meta is the effective metadata: the resource's own, with the defaults filled in.
	Meta resource.Meta
	// Digest is the content digest of the resource's files.
	Digest resource.Digest
	// Kind is the resource's kind and Spec what its Parse returned; rendering hands Spec back to
	// the kind's forms.
	Kind kind.Kind
	Spec kind.Spec
	// Path is where the resource is in the repository, or "" when it came from the server.
	Path string
}

// Reason says why a resource the source holds is not delivered. The values are stable names for
// machine-readable output.
type Reason string

const (
	// ExcludedPersonal: scope "me", which only a server can attribute to a person.
	ExcludedPersonal Reason = "personal"
	// ExcludedProduct: scoped to a product the person did not choose.
	ExcludedProduct Reason = "other_product"
	// ExcludedAudience: for an audience the person did not choose.
	ExcludedAudience Reason = "audience"
	// ExcludedNotEnabled: optional, and the person has not enabled it.
	ExcludedNotEnabled Reason = "not_enabled"
	// ExcludedDisabled: recommended, and the person disabled it.
	ExcludedDisabled Reason = "disabled"
)

// Excluded is a resource the source holds that is not for this person or computer. What an
// earlier sync wrote for it is undone.
type Excluded struct {
	Ref    resource.Ref
	Meta   resource.Meta
	Reason Reason
}

// Failed is a resource the source holds that could not be read. Its state is unknown: what an
// earlier sync wrote for it stays as it is.
type Failed struct {
	Ref    resource.Ref
	Path   string
	Issues []repoformat.Issue
}

// State is the desired state.
type State struct {
	Source Source
	// Resources are delivered, sorted by code.
	Resources []Resource
	// Excluded are not delivered, sorted by code.
	Excluded []Excluded
	// Failed could not be read, sorted by path.
	Failed []Failed
	// Notices are advisory remarks about the source, such as unknown fields.
	Notices []repoformat.Issue
}

// Lookup returns the delivered resource with the given code.
func (s *State) Lookup(code string) (Resource, bool) {
	i, ok := slices.BinarySearchFunc(s.Resources, code, func(r Resource, c string) int { return cmp.Compare(r.Ref.Code, c) })
	if !ok {
		return Resource{}, false
	}
	return s.Resources[i], true
}

// Known reports whether the source holds a resource with the given code at all: delivered,
// excluded or failed.
func (s *State) Known(code string) bool {
	if _, ok := s.Lookup(code); ok {
		return true
	}
	return slices.ContainsFunc(s.Excluded, func(e Excluded) bool { return e.Ref.Code == code }) ||
		slices.ContainsFunc(s.Failed, func(f Failed) bool { return f.Ref.Code == code })
}

// Filter is what decides which resources of a source are for this person and computer.
type Filter struct {
	// Products are the product codes whose resources are delivered.
	Products []string
	// Audiences, when not empty, limits delivery to resources for these audiences and to
	// resources that name no audience.
	Audiences []resource.Audience
	// Selection is what the person enabled and disabled on this computer.
	Selection Selection
}

// Decide says whether a resource with the given effective metadata is delivered, and if not, why.
func (f Filter) Decide(code string, m resource.Meta) (bool, Reason) {
	switch {
	case m.Scope == resource.ScopeMe:
		return false, ExcludedPersonal
	case m.Scope != "" && m.Scope != resource.ScopeOrg:
		p, _ := m.Scope.Product()
		if !slices.Contains(f.Products, p) {
			return false, ExcludedProduct
		}
	}
	if m.Audience != "" && len(f.Audiences) > 0 && !slices.Contains(f.Audiences, m.Audience) {
		return false, ExcludedAudience
	}
	switch m.Policy {
	case resource.PolicyRequired:
		return true, ""
	case resource.PolicyOptional:
		if !slices.Contains(f.Selection.Enabled, code) {
			return false, ExcludedNotEnabled
		}
	default: // recommended, or not set
		if slices.Contains(f.Selection.Disabled, code) {
			return false, ExcludedDisabled
		}
	}
	return true, ""
}

// FromRepo computes the desired state from a repository as read.
func FromRepo(repo *repoformat.Repo, src Source, f Filter) *State {
	st := &State{Source: src, Notices: slices.Clone(repo.Notices)}
	for _, r := range repo.Resources {
		ok, why := f.Decide(r.Ref.Code, r.Effective)
		if !ok {
			st.Excluded = append(st.Excluded, Excluded{Ref: r.Ref, Meta: r.Effective, Reason: why})
			continue
		}
		st.Resources = append(st.Resources, Resource{
			Ref:    r.Ref,
			Meta:   r.Effective,
			Digest: r.Digest,
			Kind:   r.Kind,
			Spec:   r.Spec,
			Path:   r.Path,
		})
	}
	for _, fl := range repo.Failed {
		st.Failed = append(st.Failed, Failed{Ref: fl.Ref, Path: fl.Path, Issues: fl.Issues})
	}
	slices.SortFunc(st.Resources, func(a, b Resource) int { return cmp.Compare(a.Ref.Code, b.Ref.Code) })
	slices.SortFunc(st.Excluded, func(a, b Excluded) int { return cmp.Compare(a.Ref.Code, b.Ref.Code) })
	return st
}
