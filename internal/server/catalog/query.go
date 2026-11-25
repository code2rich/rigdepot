// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import (
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/bundle"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Filter narrows List. Zero fields select everything.
type Filter struct {
	Kind     string
	Status   Status
	Pending  *bool // has a draft waiting
	Blocked  *bool // the draft or the live version has a must-fix finding
	Scope    resource.Scope
	SourceID string
	Query    string // matches name, code and summary, case-insensitively
}

// List returns the resources the actor manages, drafts included, by name: everything for
// platform administrators, their products' resources for owners, what they maintain for
// maintainers, and their own personal resources (the rule of access.ActionResourceDraftRead).
func (s *Service) List(ctx context.Context, a access.Actor, f Filter) ([]Resource, error) {
	where, args := []string{"1 = 1"}, []any{}
	if f.Kind != "" {
		where, args = append(where, "r.kind = ?"), append(args, f.Kind)
	}
	if f.Status != "" {
		where, args = append(where, "r.status = ?"), append(args, string(f.Status))
	}
	if f.SourceID != "" {
		where, args = append(where, "r.source_id = ?"), append(args, f.SourceID)
	}
	if f.Query != "" {
		q := "%" + strings.ToLower(f.Query) + "%"
		where = append(where, "(LOWER(r.name) LIKE ? OR LOWER(r.code) LIKE ? OR LOWER(r.summary) LIKE ?)")
		args = append(args, q, q, q)
	}
	rows, err := s.loadRows(ctx, s.db, strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, err
	}
	out := []Resource{}
	for _, r := range rows {
		switch {
		case f.Pending != nil && r.HasDraft != *f.Pending,
			f.Blocked != nil && r.Blocked != *f.Blocked,
			f.Scope != "" && r.Scope != f.Scope:
			continue
		}
		d := s.access.Can(ctx, a, access.ActionResourceDraftRead, r.target())
		if d.Err != nil {
			return nil, d.Err
		}
		if d.Allowed {
			out = append(out, s.withDiffers(ctx, s.db, r))
		}
	}
	return out, nil
}

// managed reads one resource and checks that the actor manages it. A resource the actor may
// not see at all is ErrNotFound, one they may see but not manage ErrDenied.
func (s *Service) managed(ctx context.Context, a access.Actor, code string) (resourceRow, error) {
	r, err := s.loadRow(ctx, s.db, code)
	if err != nil {
		return r, err
	}
	if err := s.can(ctx, a, access.ActionResourceDraftRead, r.target()); err != nil {
		if errors.Is(err, ErrDenied) && s.can(ctx, a, access.ActionResourceRead, r.target()) != nil {
			return r, ErrNotFound
		}
		return r, err
	}
	return r, nil
}

// Get returns one resource as its managers see it.
func (s *Service) Get(ctx context.Context, a access.Actor, code string) (Resource, error) {
	r, err := s.managed(ctx, a, code)
	if err != nil {
		return Resource{}, err
	}
	return s.withDiffers(ctx, s.db, r), nil
}

// Draft returns the draft waiting to be published, with its files; ErrNoDraft when there is
// none.
func (s *Service) Draft(ctx context.Context, a access.Actor, code string) (Draft, error) {
	r, err := s.managed(ctx, a, code)
	if err != nil {
		return Draft{}, err
	}
	d, has, err := s.draft(ctx, s.db, r.Resource)
	if err != nil {
		return Draft{}, err
	}
	if !has {
		return Draft{}, ErrNoDraft
	}
	fsys, err := files(ctx, s.db, d.Bundle)
	if err != nil {
		return Draft{}, err
	}
	d.Files = fileList(fsys)
	return d, nil
}

// Definition returns the document of a one-document kind as it is in the content at address
// (without its meta), and the settings each computer fills in for itself (.env.example).
func (s *Service) Definition(ctx context.Context, kindID, code, address string) (map[string]any, []LocalConfig, error) {
	fsys, err := files(ctx, s.db, address)
	if err != nil {
		return nil, nil, err
	}
	k, _ := s.lookupKind(kindID)
	def, err := definition(k, fsys, code)
	if err != nil {
		def = map[string]any{}
	}
	return def, localConfig(fsys), nil
}

// DraftFile returns one file of the draft.
func (s *Service) DraftFile(ctx context.Context, a access.Actor, code, path string) ([]byte, error) {
	d, err := s.Draft(ctx, a, code)
	if err != nil {
		return nil, err
	}
	return readFile(ctx, s.db, d.Bundle, path)
}

func readFile(ctx context.Context, q dbx.Querier, address, path string) ([]byte, error) {
	fsys, err := files(ctx, q, address)
	if err != nil {
		return nil, err
	}
	if !fs.ValidPath(path) {
		return nil, invalid(FieldError{Field: "path", Code: "invalid"})
	}
	data, err := fsys.ReadFile(path)
	if err != nil {
		return nil, ErrNotFound
	}
	return data, nil
}

// FileChange is how one file differs between the live version and the draft.
type FileChange struct {
	Path    string
	Change  string // added, removed, modified
	Unified string // a unified diff, for text files that were modified
}

// DraftDiff returns, file by file, what the draft changes compared with the live version.
func (s *Service) DraftDiff(ctx context.Context, a access.Actor, code string) (base int, changes []FileChange, err error) {
	d, err := s.Draft(ctx, a, code)
	if err != nil {
		return 0, nil, err
	}
	newFiles, err := files(ctx, s.db, d.Bundle)
	if err != nil {
		return 0, nil, err
	}
	oldFiles := &bundle.Files{}
	if d.BaseVersion > 0 {
		r, err := s.loadRow(ctx, s.db, code)
		if err != nil {
			return 0, nil, err
		}
		v, err := s.version(ctx, s.db, r.ID, d.BaseVersion)
		if err != nil {
			return 0, nil, err
		}
		if oldFiles, err = files(ctx, s.db, v.Bundle); err != nil {
			return 0, nil, err
		}
	}
	paths := append(oldFiles.Paths(), newFiles.Paths()...)
	slices.Sort(paths)
	changes = []FileChange{}
	for _, p := range slices.Compact(paths) {
		was, errOld := oldFiles.ReadFile(p)
		now, errNew := newFiles.ReadFile(p)
		switch {
		case errOld != nil:
			changes = append(changes, FileChange{Path: p, Change: "added"})
		case errNew != nil:
			changes = append(changes, FileChange{Path: p, Change: "removed"})
		case string(was) != string(now):
			changes = append(changes, FileChange{Path: p, Change: "modified", Unified: unifiedDiff(was, now)})
		}
	}
	return d.BaseVersion, changes, nil
}

// Versions returns a resource's versions, newest first, limit at a time (50 when 0). cursor is
// the next cursor of the previous page, "" for the first.
func (s *Service) Versions(ctx context.Context, a access.Actor, code string, limit int, cursor string) ([]Version, string, error) {
	r, err := s.managed(ctx, a, code)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 {
		limit = 50
	}
	below := int(^uint(0) >> 1)
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		n, convErr := strconv.Atoi(string(raw))
		if err != nil || convErr != nil || n < 1 {
			return nil, "", invalid(FieldError{Field: "cursor", Code: "invalid"})
		}
		below = n
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+versionCols+` FROM resource_versions WHERE resource_id = ? AND number < ?
		ORDER BY number DESC LIMIT ?`, r.ID, below, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	ws, err := s.waivers(ctx, s.db, r.ID)
	if err != nil {
		return nil, "", err
	}
	for i := range out {
		applyWaivers(&out[i].Checks, ws)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(out[limit-1].Number)))
	}
	return out, next, nil
}

// Version returns one published version with its check results.
func (s *Service) Version(ctx context.Context, a access.Actor, code string, n int) (Version, error) {
	r, err := s.managed(ctx, a, code)
	if err != nil {
		return Version{}, err
	}
	return s.version(ctx, s.db, r.ID, n)
}

// Impact says who loses a resource if it is disabled (scope nil) or narrowed to scope:
// installed holds, by user id, on how many computers each person has it (package delivery),
// and the workflows and agents that refer to it.
func (s *Service) Impact(ctx context.Context, a access.Actor, code string, scope *resource.Scope, installed map[string]int) (Impact, error) {
	r, err := s.managed(ctx, a, code)
	if err != nil {
		return Impact{}, err
	}
	keeps := func(string) bool { return false }
	if scope != nil {
		switch *scope {
		case resource.ScopeOrg:
			keeps = func(string) bool { return true }
		case resource.ScopeMe:
			keeps = func(u string) bool { return u == r.OwnerID }
		default:
			members, err := s.access.ListMembers(ctx, access.System, string(*scope))
			if err != nil {
				return Impact{}, invalid(FieldError{Field: "scope", Code: "unknown"})
			}
			in := map[string]bool{}
			for _, m := range members {
				in[m.User.ID] = true
			}
			keeps = func(u string) bool { return in[u] }
		}
	}
	var im Impact
	for user, devices := range installed {
		if devices > 0 && !keeps(user) {
			im.People++
			im.Devices += devices
		}
	}
	im.ReferencedBy, err = s.ReferencedBy(ctx, code)
	return im, err
}

// ReferencedBy lists the resources whose live version refers to code.
func (s *Service) ReferencedBy(ctx context.Context, code string) ([]Ref, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.code, r.kind, r.name FROM resource_refs f JOIN resources r ON r.id = f.resource_id
		WHERE f.ref_code = ? ORDER BY r.name, r.code`, code)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ref{}
	for rows.Next() {
		var ref Ref
		if err := rows.Scan(&ref.Code, &ref.Kind, &ref.Name); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// Refs returns the short references of the given codes that exist, in order.
func (s *Service) Refs(ctx context.Context, codes []string) ([]Ref, error) {
	out := []Ref{}
	for _, c := range codes {
		var ref Ref
		err := s.db.QueryRowContext(ctx, `SELECT code, kind, name FROM resources WHERE code = ?`, c).Scan(&ref.Code, &ref.Kind, &ref.Name)
		if err == nil {
			out = append(out, ref)
		}
	}
	return out, nil
}

// CatalogFilter narrows Catalog.
type CatalogFilter struct {
	Kind     string
	Stage    string
	Audience resource.Audience
	Query    string
}

// Catalog returns the published resources delivered to the actor, by name: those of the
// organisation, of the products they are in and their own personal ones. Visibility is read
// fresh: someone taken out of a product no longer sees its resources.
func (s *Service) Catalog(ctx context.Context, a access.Actor, f CatalogFilter) ([]Resource, error) {
	v, err := s.access.VisibilityOf(ctx, a)
	if err != nil {
		return nil, err
	}
	if !v.Active {
		return []Resource{}, nil
	}
	where := []string{"r.status = 'published'"}
	args := []any{}
	vis := []string{"r.scope_kind = 'org'", "(r.scope_kind = 'me' AND r.owner_user_id = ?)"}
	args = append(args, v.UserID)
	if len(v.Products) > 0 {
		vis = append(vis, "r.scope_product_id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(v.Products)), ",")+")")
		for _, p := range v.Products {
			args = append(args, p)
		}
	}
	where = append(where, "("+strings.Join(vis, " OR ")+")")
	if f.Kind != "" {
		where, args = append(where, "r.kind = ?"), append(args, f.Kind)
	}
	if f.Audience != "" {
		where, args = append(where, "r.audience = ?"), append(args, string(f.Audience))
	}
	if f.Query != "" {
		q := "%" + strings.ToLower(f.Query) + "%"
		where = append(where, "(LOWER(r.name) LIKE ? OR LOWER(r.code) LIKE ? OR LOWER(r.summary) LIKE ?)")
		args = append(args, q, q, q)
	}
	rows, err := s.loadRows(ctx, s.db, strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, err
	}
	out := []Resource{}
	for _, r := range rows {
		if f.Stage == "" || slices.Contains(r.Stages, f.Stage) {
			out = append(out, r.Resource)
		}
	}
	return out, nil
}

// Published returns one published resource the actor may see, with its live version. A
// resource that is not published or not visible is ErrNotFound.
func (s *Service) Published(ctx context.Context, a access.Actor, code string) (Resource, Version, error) {
	r, err := s.loadRow(ctx, s.db, code)
	if err != nil {
		return Resource{}, Version{}, err
	}
	if r.Status != StatusPublished || r.Version == 0 {
		return Resource{}, Version{}, ErrNotFound
	}
	if err := s.can(ctx, a, access.ActionResourceRead, r.target()); err != nil {
		if errors.Is(err, ErrDenied) {
			return Resource{}, Version{}, ErrNotFound
		}
		return Resource{}, Version{}, err
	}
	v, err := s.version(ctx, s.db, r.ID, r.Version)
	return r.Resource, v, err
}

// Files lists the files stored under a bundle address.
func (s *Service) Files(ctx context.Context, address string) ([]File, error) {
	fsys, err := files(ctx, s.db, address)
	if err != nil {
		return nil, err
	}
	return fileList(fsys), nil
}

// PublishedFile returns one file of a published version (the live one when version is 0) of a
// resource the actor may see.
func (s *Service) PublishedFile(ctx context.Context, a access.Actor, code string, version int, path string) ([]byte, error) {
	r, live, err := s.Published(ctx, a, code)
	if err != nil {
		return nil, err
	}
	v := live
	if version > 0 && version != live.Number {
		if v, err = s.version(ctx, s.db, r.ID, version); err != nil {
			return nil, err
		}
	}
	return readFile(ctx, s.db, v.Bundle, path)
}

// Bundle returns the bytes of a stored bundle by its address, for delivery to the command line
// (package delivery decides who may fetch which).
func (s *Service) Bundle(ctx context.Context, address string) ([]byte, error) {
	return blob(ctx, s.db, address)
}

// Rule is one rule of the rule set, as the catalog applies it.
type Rule struct {
	ID       string
	Severity extcheck.Severity
	Waivable bool
}

// Rules returns the rule set every resource is checked against, and its version.
func (s *Service) Rules() (int, []Rule) {
	out := make([]Rule, 0, len(s.rules.Rules))
	for _, r := range s.rules.Rules {
		out = append(out, Rule{ID: r.ID(), Severity: r.Severity(), Waivable: r.ID() == secretsRule})
	}
	return s.rules.Version, out
}

// Stages returns the stages of the delivery process in order: the default ones (ext/resource)
// first, then the ones resources named that the server had not seen, by when they appeared. A
// hidden stage is listed and marked. The default stages have no name of their own (their names
// come from the translations); the others are named by their identifier.
func (s *Service) Stages(ctx context.Context) ([]resource.Stage, error) {
	out := resource.DefaultStages()
	rows, err := s.db.QueryContext(ctx, `SELECT code, name, hidden FROM stages ORDER BY position, created_at, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st resource.Stage
		if err := rows.Scan(&st.ID, &st.Name, &st.Hidden); err != nil {
			return nil, err
		}
		if i := slices.IndexFunc(out, func(x resource.Stage) bool { return x.ID == st.ID }); i >= 0 {
			out[i].Hidden = st.Hidden
			if st.Name != "" {
				out[i].Name = st.Name
			}
			continue
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// ResourceCounts returns, by product id, how many resources that are not disabled are scoped to
// each product.
func (s *Service) ResourceCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT scope_product_id, COUNT(*) FROM resources
		WHERE scope_kind = 'product' AND scope_product_id IS NOT NULL AND status <> 'disabled' GROUP BY scope_product_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// Target describes a resource for a permission decision about it, such as whether someone may
// read its audit log: false when there is no resource with the code.
func (s *Service) Target(ctx context.Context, code string) (access.Target, bool, error) {
	r, found, err := s.findRow(ctx, s.db, code)
	if err != nil || !found {
		return access.Target{}, false, err
	}
	return r.target(), true, nil
}

// SourceName returns the display name of a resource source, or its id when it is gone.
func (s *Service) SourceName(ctx context.Context, id string) string {
	return s.sourceName(ctx, s.db, id)
}

// Live is the live version of a resource as package delivery hands it out: its content digest,
// the address of its bundle and the bundle's size in bytes.
type Live struct {
	Version int
	Digest  resource.Digest
	Bundle  string
	Size    int64
}

// LiveVersions returns, by resource id, the live version of each published resource among ids.
// It checks no permission: the caller has found the resources with Catalog.
func (s *Service) LiveVersions(ctx context.Context, ids []string) (map[string]Live, error) {
	out := map[string]Live{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, v.number, v.digest, v.bundle, b.size
		FROM resources r
		JOIN resource_versions v ON v.resource_id = r.id AND v.number = r.current_version
		JOIN blobs b ON b.digest = v.bundle
		WHERE r.status = 'published' AND r.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var l Live
		if err := rows.Scan(&id, &l.Version, &l.Digest, &l.Bundle, &l.Size); err != nil {
			return nil, err
		}
		out[id] = l
	}
	return out, rows.Err()
}

// BundleResources returns the published resources that have a version, live or earlier, stored
// in the bundle at address. Package delivery hands a bundle only to someone who sees one of them.
func (s *Service) BundleResources(ctx context.Context, address string) ([]Resource, error) {
	rows, err := s.loadRows(ctx, s.db, `r.status = 'published' AND r.id IN (SELECT resource_id FROM resource_versions WHERE bundle = ?)`, address)
	if err != nil {
		return nil, err
	}
	out := make([]Resource, len(rows))
	for i, r := range rows {
		out[i] = r.Resource
	}
	return out, nil
}

// ResourceIDs returns the ids of the resources with the given codes, by code; codes the catalog
// does not have are left out.
func (s *Service) ResourceIDs(ctx context.Context, codes []string) (map[string]string, error) {
	out := map[string]string{}
	if len(codes) == 0 {
		return out, nil
	}
	args := make([]any, len(codes))
	for i, c := range codes {
		args[i] = c
	}
	rows, err := s.db.QueryContext(ctx, `SELECT code, id FROM resources WHERE code IN (`+strings.TrimSuffix(strings.Repeat("?,", len(codes)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code, id string
		if err := rows.Scan(&code, &id); err != nil {
			return nil, err
		}
		out[code] = id
	}
	return out, rows.Err()
}

// Visible reports whether someone with visibility v gets a resource: it is published and in a
// scope v includes.
func (r Resource) Visible(v access.Visibility) bool {
	return r.Status == StatusPublished && v.Includes(r.target())
}
