// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import (
	"cmp"
	"context"
	"slices"

	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/check"
	"github.com/code2rich/rigdepot/internal/repoformat"
)

// candidate is one resource of a repository after reading and checking: what a draft is made
// of. A refused one (the reader could not use it) has no content, only its findings.
type candidate struct {
	Path     string
	Ref      resource.Ref
	Refused  bool
	Issues   []repoformat.Issue // why it was refused
	Resource repoformat.Resource
	Meta     Meta
	Report   Report
}

// references returns the codes of the resources a parsed resource uses (a workflow's steps, an
// agent's bindings), from its kind.Referrer; they come along when it is delivered.
func references(spec kind.Spec) []string {
	if ref, ok := spec.(kind.Referrer); ok {
		return dedupe(ref.Refs())
	}
	return nil
}

// checkRepo checks every resource of a repository the way rgd check does (internal/cli,
// runChecks): the same engine and rules, the resources of the repository as each other's
// peers (plus extra, for a resource that comes alone), a refused resource as must-fix findings
// of the structure rule (check.Refused), the reader's notices about a resource's files as
// advisory findings of it, and the findings' identifiers from check.FindingIDs. Only the
// rendering differs.
func (s *Service) checkRepo(ctx context.Context, repo *repoformat.Repo, extra extcheck.Peers) ([]candidate, error) {
	engine, err := s.checker()
	if err != nil {
		return nil, err
	}
	located, err := check.FromRepo(repo)
	if err != nil {
		return nil, err
	}
	all := make([]check.Resource, len(located))
	for i, l := range located {
		all[i] = l.Resource
	}
	type item struct {
		c        candidate
		res      check.Resource
		findings []extcheck.Finding
	}
	var items []*item
	for _, l := range located {
		peers := append(check.PeersOf(all, l.Ref), extra...)
		peers = slices.DeleteFunc(peers, func(p extcheck.Peer) bool { return p.Ref.Code == l.Ref.Code })
		r, err := engine.Check(ctx, l.Resource, peers)
		if err != nil {
			return nil, err
		}
		rr, _ := repo.Lookup(l.Ref.Code)
		meta := Meta{Meta: rr.Effective, Description: check.Description(l.Resource)}
		meta.References = references(rr.Spec)
		items = append(items, &item{c: candidate{Path: l.Path, Ref: l.Ref, Resource: rr, Meta: meta}, res: l.Resource, findings: r.Findings})
	}
	for _, f := range repo.Failed {
		r := check.Refused(f, engine.Version())
		items = append(items, &item{c: candidate{Path: f.Path, Ref: f.Ref, Refused: true, Issues: f.Issues}, findings: r.Findings})
	}
	for _, n := range repo.Notices {
		i := slices.IndexFunc(items, func(it *item) bool { return check.Within(n.File, it.c.Path) })
		if i >= 0 {
			items[i].findings = append(items[i].findings, check.IssueFinding(n, items[i].c.Path, extcheck.Advisory))
		}
	}
	slices.SortFunc(items, func(x, y *item) int { return cmp.Compare(x.c.Path, y.c.Path) })
	out := make([]candidate, len(items))
	for i, it := range items {
		ids := check.FindingIDs(it.res, it.findings)
		rep := Report{Ruleset: engine.Version(), Findings: make([]Finding, len(it.findings))}
		for j, f := range it.findings {
			rep.Findings[j] = Finding{ID: ids[j], Rule: f.Rule, Severity: f.Severity, Message: f.Message, Params: f.Params, File: f.File, Line: f.Line}
		}
		it.c.Report = rep
		if !it.c.Refused {
			it.c.Meta.Delivery = deliveryOf(it.c.Resource)
		}
		out[i] = it.c
	}
	return out, nil
}

// deliveryOf reads the delivery of an MCP server from its definition ("delivery: native").
func deliveryOf(r repoformat.Resource) Delivery {
	if r.Kind == nil || !r.Kind.Layout().File {
		return ""
	}
	def, err := definition(r.Kind, r.Files, r.Ref.Code)
	if err != nil {
		return ""
	}
	if d, ok := def["delivery"].(string); ok && (d == string(DeliveryNative) || d == string(DeliveryOnDemand)) {
		return Delivery(d)
	}
	return ""
}

// lookupKind finds a kind by its id among the service's kinds.
func (s *Service) lookupKind(id string) (kind.Kind, bool) {
	var all []kind.Kind
	if s.kinds != nil {
		all = s.kinds.All()
	} else {
		all = kind.All()
	}
	for _, k := range all {
		if k.ID() == id {
			return k, true
		}
	}
	return nil, false
}

// carryFirstSeen keeps, for each finding that was already in an earlier state of the resource
// (same identifier), when it was first seen, so that "present for N weeks" survives new
// drafts; new findings are first seen now.
func carryFirstSeen(rep *Report, earlier []Finding, now int64) {
	seen := map[string]int64{}
	for _, f := range earlier {
		if f.FirstSeen > 0 && (seen[f.ID] == 0 || f.FirstSeen < seen[f.ID]) {
			seen[f.ID] = f.FirstSeen
		}
	}
	for i := range rep.Findings {
		if t := seen[rep.Findings[i].ID]; t > 0 {
			rep.Findings[i].FirstSeen = t
		} else {
			rep.Findings[i].FirstSeen = now
		}
	}
}
