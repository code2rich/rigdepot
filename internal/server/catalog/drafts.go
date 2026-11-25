// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"

	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/bundle"
	"github.com/code2rich/rigdepot/internal/repoformat"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/events"
)

// Every way content comes in (an import from a repository, an upload, a registration on the
// web) ends in saveDraft with a candidate that has been read and checked by checkRepo.

// draftInput is a checked candidate and where it comes from.
type draftInput struct {
	c        candidate
	origin   Origin
	sourceID string
	commit   string
	// ifMatch is the draft ETag a person based the change on. Checked only for uploads and the
	// web (people); an import replaces the draft without asking.
	ifMatch      *string
	checkIfMatch bool
}

// DraftResult is what saving a draft did.
type DraftResult struct {
	Draft   Draft
	Created bool // the draft created the resource
	Changed bool // false: the content was already the draft's (or, for an import, the live version's)
}

// saveDraft stores a candidate as the resource's draft, creating the resource with the first
// draft, in one transaction with its audit entry.
//
// An import changes nothing when the content is already the draft's, or the live version's;
// in the second case a draft that differs (an intermediate commit the repository went back
// from) is discarded. That is what makes imports idempotent.
func (s *Service) saveDraft(ctx context.Context, a access.Actor, in draftInput) (DraftResult, error) {
	c := in.c
	packed, err := bundle.Pack(c.Resource.Files)
	if err != nil {
		return DraftResult{}, fmt.Errorf("catalog: pack %s: %w", c.Path, err)
	}
	productIDs, err := s.productIDs(ctx)
	if err != nil {
		return DraftResult{}, err
	}
	var res DraftResult
	err = s.write(ctx, a, func(o *op) error {
		now := s.millis()
		row, found, err := s.findRow(ctx, o.tx, c.Ref.Code)
		if err != nil {
			return err
		}
		obj := resourceObject(c.Ref.Code, c.Meta.Name)
		var earlier []Finding
		if found {
			obj.Label = row.Name
			if row.Kind != c.Ref.Kind {
				return ErrKindMismatch.with(map[string]any{"kind": row.Kind})
			}
			adopt := in.origin == OriginRepo && row.Origin == OriginRepo && row.SourceID == ""
			switch {
			case adopt:
				// The repository it came from was removed (DeleteSource): this one takes it over.
			case in.origin == OriginRepo && (row.Origin != OriginRepo || row.SourceID != in.sourceID):
				return ErrCodeTaken.with(map[string]any{"code": row.Code})
			case in.origin != OriginRepo && row.Origin == OriginRepo:
				return ErrFromRepository.with(map[string]any{"source": s.sourceName(ctx, o.tx, row.SourceID)})
			}
			if err := o.check(access.ActionResourceEdit, row.target(), obj); err != nil {
				return err
			}
			if adopt {
				if _, err := o.tx.ExecContext(ctx, `UPDATE resources SET source_id = ? WHERE id = ?`, in.sourceID, row.ID); err != nil {
					return err
				}
				if err := s.touch(o, row.ID, now); err != nil {
					return err
				}
				if err := o.record(access.ActionResourceEdit, obj, map[string]any{"source": in.sourceID}); err != nil {
					return err
				}
				row.SourceID, res.Changed = in.sourceID, true
			}
			d, has, err := s.draft(ctx, o.tx, row.Resource)
			if err != nil {
				return err
			}
			if in.checkIfMatch && has {
				switch {
				case in.ifMatch == nil:
					return ErrPreconditionRequired
				case *in.ifMatch != d.ETag():
					return ErrPreconditionFailed
				}
			}
			var live Version
			if row.Version > 0 {
				if live, err = s.version(ctx, o.tx, row.ID, row.Version); err != nil {
					return err
				}
			}
			if has && d.Digest == c.Resource.Digest && d.Checks.Ruleset == s.rules.Version && in.origin == OriginRepo {
				res.Draft = d
				return nil
			}
			if in.origin == OriginRepo && row.Version > 0 && live.Digest == c.Resource.Digest {
				if has {
					if _, err := o.tx.ExecContext(ctx, `DELETE FROM resource_drafts WHERE resource_id = ?`, row.ID); err != nil {
						return err
					}
					if err := s.touch(o, row.ID, now); err != nil {
						return err
					}
					res.Changed = true
					return o.record(access.ActionResourceEdit, obj, map[string]any{"draft": "discarded", "reason": "same_as_live", "version": row.Version})
				}
				return nil
			}
			earlier = append(d.Checks.Findings, live.Checks.Findings...)
		} else {
			row, err = s.createRow(o, c, in, productIDs, now)
			if err != nil {
				return err
			}
			res.Created = true
		}

		rep := c.Report
		carryFirstSeen(&rep, earlier, now)
		if err := putBlob(ctx, o.tx, packed, now); err != nil {
			return err
		}
		var updatedBy any
		if a.UserID != "" {
			updatedBy = a.UserID
		}
		if _, err := o.tx.ExecContext(ctx, `INSERT INTO resource_drafts (resource_id, digest, bundle, meta, ruleset, findings, source_commit, updated_by, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (resource_id) DO UPDATE SET digest = excluded.digest, bundle = excluded.bundle, meta = excluded.meta,
				ruleset = excluded.ruleset, findings = excluded.findings, source_commit = excluded.source_commit,
				updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			row.ID, string(c.Resource.Digest), packed.Address, encodeJSON(c.Meta), rep.Ruleset, encodeJSON(rep.Findings), in.commit, updatedBy, now); err != nil {
			return err
		}
		if _, err := o.tx.ExecContext(ctx, `UPDATE resources SET origin = ?, source_path = ? WHERE id = ?`, string(in.origin), sourcePath(in), row.ID); err != nil {
			return err
		}
		if row.Status == StatusDraft {
			// Before the first publish the resource is what its draft says.
			if row, err = s.applyContent(o, row, c.Meta, productIDs); err != nil {
				return err
			}
		}
		if err := s.touch(o, row.ID, now); err != nil {
			return err
		}
		ws, err := s.waivers(ctx, o.tx, row.ID)
		if err != nil {
			return err
		}
		applyWaivers(&rep, ws)
		res.Changed = true
		res.Draft = Draft{Code: row.Code, Kind: row.Kind, Digest: c.Resource.Digest, Bundle: packed.Address, Meta: c.Meta,
			Checks: rep, BaseVersion: row.Version, Commit: in.commit, UpdatedBy: a.UserID, UpdatedAt: dbx.Time(now)}
		action := access.ActionResourceEdit
		if res.Created {
			action = access.ActionResourceCreate
		}
		change := map[string]any{"draft": string(c.Resource.Digest), "origin": string(in.origin), "blocking": rep.Counts().Blocking}
		if in.commit != "" {
			change["commit"] = in.commit
		}
		if err := o.record(action, obj, change); err != nil {
			return err
		}
		updated, err := s.loadRow(ctx, o.tx, row.Code)
		if err != nil {
			return err
		}
		o.emit(events.ResourceDraftUpdated{ResourceRef: updated.ref(), Digest: string(c.Resource.Digest), Created: res.Created, ActorID: a.UserID})
		return nil
	})
	if err != nil {
		return DraftResult{}, err
	}
	if res.Draft.Code != "" && res.Draft.Files == nil {
		res.Draft.Files = fileList(mustFiles(c.Resource.Files))
	}
	return res, nil
}

func sourcePath(in draftInput) string {
	if in.origin == OriginRepo {
		return in.c.Path
	}
	return ""
}

// mustFiles lists the regular files of fsys with their sizes, as a bundle.Files would.
func mustFiles(fsys fs.FS) *bundle.Files {
	f, err := bundle.Snapshot(fsys)
	if err != nil {
		return &bundle.Files{}
	}
	return f
}

// touch marks a change of the resource: a new revision (and so a new ETag).
func (s *Service) touch(o *op, id string, now int64) error {
	_, err := o.tx.ExecContext(o.ctx, `UPDATE resources SET revision = revision + 1, updated_at = ? WHERE id = ?`, now, id)
	return err
}

// createRow creates a resource for its first draft, after checking that the actor may create
// one in the scope the draft names.
func (s *Service) createRow(o *op, c candidate, in draftInput, productIDs map[string]string, now int64) (resourceRow, error) {
	r := resourceRow{Resource: Resource{ID: dbx.NewID(), Code: c.Ref.Code, Kind: c.Ref.Kind, Origin: in.origin, SourceID: in.sourceID, Status: StatusDraft}}
	r.Scope, r.ProductID, r.OwnerID = scopeOf(c.Meta.Scope, productIDs, o.actor)
	if r.Scope == resource.ScopeMe && r.OwnerID == "" {
		// A personal resource belongs to a person: an import has nobody to give it to.
		return r, invalid(FieldError{Field: "meta.scope", Code: "personal_needs_author"})
	}
	obj := resourceObject(c.Ref.Code, c.Meta.Name)
	if err := o.check(access.ActionResourceCreate, r.target(), obj); err != nil {
		return r, err
	}
	var source, product, owner any
	if in.sourceID != "" {
		source = in.sourceID
	}
	if r.ProductID != "" {
		product = r.ProductID
	}
	if r.OwnerID != "" {
		owner = r.OwnerID
	}
	_, err := o.tx.ExecContext(o.ctx, `INSERT INTO resources (id, code, kind, origin, source_id, scope_kind, scope_product_id, owner_user_id,
		policy, audience, status, name, summary, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Code, r.Kind, string(in.origin), source, scopeKind(r.Scope), product, owner,
		string(resource.PolicyRecommended), string(resource.AudienceRD), string(StatusDraft), c.Meta.Name, c.Meta.Summary, now, now)
	if dbx.IsUniqueViolation(err) {
		return r, ErrCodeTaken.with(map[string]any{"code": r.Code})
	}
	r.overrideBase = map[string]json.RawMessage{}
	return r, err
}

// scopeOf resolves a scope for the resources table: "org" (also for an empty scope), "me"
// (the actor's), or a product by its code. A product the server does not have keeps its code
// in the metadata and no product id: only platform administrators see it until it exists.
func scopeOf(scope resource.Scope, productIDs map[string]string, a access.Actor) (resource.Scope, string, string) {
	switch scope {
	case "", resource.ScopeOrg:
		return resource.ScopeOrg, "", ""
	case resource.ScopeMe:
		return resource.ScopeMe, "", a.UserID
	}
	return scope, productIDs[string(scope)], ""
}

func scopeKind(s resource.Scope) string {
	switch s {
	case resource.ScopeOrg, resource.ScopeMe:
		return string(s)
	}
	return string(access.InProduct)
}

// applyContent makes the resource what content metadata says, except for the fields
// overridden on the platform: at the first draft, at every draft before the first publish, at
// publishing and at a rollback.
func (s *Service) applyContent(o *op, r resourceRow, m Meta, productIDs map[string]string) (resourceRow, error) {
	over := func(f string) bool { return slices.Contains(r.Overrides, f) }
	eff := r.Meta
	set := func(f string, apply func()) {
		if !over(f) {
			apply()
		}
	}
	set(FieldName, func() { eff.Name = m.Name })
	set(FieldSummary, func() { eff.Summary = m.Summary })
	set(FieldStages, func() { eff.Stages = m.Stages })
	set(FieldAudience, func() { eff.Audience = m.Audience })
	set(FieldPolicy, func() { eff.Policy = m.Policy })
	set(FieldWhen, func() { eff.When = m.When })
	set(FieldTry, func() { eff.Try = m.Try })
	set(FieldDelivery, func() { eff.Delivery = m.Delivery })
	set(FieldMaintainers, func() { eff.Maintainers = m.Maintainers })
	set(FieldScope, func() {
		if m.Scope == resource.ScopeMe && r.OwnerID == "" {
			return // only a resource created as personal has an author
		}
		eff.Scope = m.Scope
	})
	// What is not overridable comes from the content as it is.
	eff.Secrets, eff.Tags, eff.I18n, eff.Description, eff.References = m.Secrets, m.Tags, m.I18n, m.Description, m.References
	if eff.Audience == "" {
		eff.Audience = resource.AudienceRD
	}
	if eff.Policy == "" {
		eff.Policy = resource.PolicyRecommended
	}
	if eff.Scope == "" {
		eff.Scope = resource.ScopeOrg
	}
	if r.Scope == resource.ScopeMe && eff.Scope != resource.ScopeMe && !over(FieldScope) {
		// A personal resource is shared by asking (M2), not by editing its metadata.
		eff.Scope = resource.ScopeMe
	}
	return s.storeSettings(o, r, eff, productIDs)
}

// storeSettings writes the metadata in force and the columns that repeat it.
func (s *Service) storeSettings(o *op, r resourceRow, eff Meta, productIDs map[string]string) (resourceRow, error) {
	scope, productID, _ := scopeOf(eff.Scope, productIDs, access.Actor{})
	var product any
	if productID != "" {
		product = productID
	}
	if _, err := o.tx.ExecContext(o.ctx, `UPDATE resources SET name = ?, summary = ?, scope_kind = ?, scope_product_id = ?, policy = ?,
		audience = ?, meta = ? WHERE id = ?`,
		eff.Name, eff.Summary, scopeKind(scope), product, string(eff.Policy), string(eff.Audience), encodeJSON(eff), r.ID); err != nil {
		return r, err
	}
	now := s.millis()
	if err := setStages(o.ctx, o.tx, r.ID, eff.Stages, now); err != nil {
		return r, err
	}
	if !slices.Contains(r.Overrides, FieldMaintainers) {
		ids, err := s.userIDs(o.ctx, eff.Maintainers)
		if err != nil {
			return r, err
		}
		if err := setMaintainers(o.ctx, o.tx, r.ID, ids); err != nil {
			return r, err
		}
	}
	r.Meta, r.Name, r.Summary, r.Scope, r.ProductID = eff, eff.Name, eff.Summary, scope, productID
	r.Policy, r.Audience, r.Stages = eff.Policy, eff.Audience, eff.Stages
	return r, nil
}

// productIDs maps product codes to ids.
func (s *Service) productIDs(ctx context.Context) (map[string]string, error) {
	ps, err := s.access.ListProducts(ctx, access.System)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(ps))
	for _, p := range ps {
		out[p.Code] = p.ID
	}
	return out, nil
}

// userIDs maps account names to user ids; names that are not accounts are left out.
func (s *Service) userIDs(ctx context.Context, usernames []string) ([]string, error) {
	if len(usernames) == 0 {
		return nil, nil
	}
	us, err := s.access.ListUsers(ctx, access.System, access.UserFilter{})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range usernames {
		for _, u := range us {
			if u.Username == name {
				out = append(out, u.ID)
			}
		}
	}
	return out, nil
}

func (s *Service) sourceName(ctx context.Context, q dbx.Querier, id string) string {
	var name string
	if err := q.QueryRowContext(ctx, `SELECT name FROM sources WHERE id = ?`, id).Scan(&name); err != nil {
		return id
	}
	return name
}

// ImportSource is where an import comes from.
type ImportSource struct {
	SourceID string // the sources row
	Commit   string // the commit the repository was read at
}

// ImportFailure is a resource of the repository that was not imported, with why.
type ImportFailure struct {
	Path     string
	Code     string // as found; may not be a valid code
	Kind     string
	Error    error     // a refusal of the catalog (ErrCodeTaken, ErrKindMismatch…); nil when the reader refused it
	Findings []Finding // the reader's reasons, as must-fix findings of the structure rule
}

// ImportResult is what one import did.
type ImportResult struct {
	Imported  []string // codes whose draft was created or replaced (or discarded as equal to the live version)
	Unchanged []string // codes whose content was already the draft's or the live version's
	Failed    []ImportFailure
}

// ImportRepo imports the resources of a repository as drafts: the import a resource source
// (M1-25) runs after reading a commit. Each resource is checked like rgd check does and saved in
// its own transaction, so one that fails does not stop the others; a resource the reader
// refused keeps whatever the catalog had for it, and resources missing from the repository are
// not touched: a failed or partial read never deletes anything.
//
// Running it again on the same commit changes nothing.
func (s *Service) ImportRepo(ctx context.Context, a access.Actor, src ImportSource, repo *repoformat.Repo) (ImportResult, error) {
	var res ImportResult
	if src.SourceID == "" {
		return res, errors.New("catalog: ImportRepo needs a source")
	}
	if err := s.ensureStages(ctx, repo.Config.Stages); err != nil {
		return res, err
	}
	cs, err := s.checkRepo(ctx, repo, nil)
	if err != nil {
		return res, err
	}
	for _, c := range cs {
		if c.Refused {
			res.Failed = append(res.Failed, ImportFailure{Path: c.Path, Code: c.Ref.Code, Kind: c.Ref.Kind, Findings: c.Report.Findings})
			continue
		}
		r, err := s.saveDraft(ctx, a, draftInput{c: c, origin: OriginRepo, sourceID: src.SourceID, commit: src.Commit})
		var ce *Error
		switch {
		case errors.As(err, &ce):
			res.Failed = append(res.Failed, ImportFailure{Path: c.Path, Code: c.Ref.Code, Kind: c.Ref.Kind, Error: err})
		case err != nil:
			return res, err
		case r.Changed:
			res.Imported = append(res.Imported, c.Ref.Code)
		default:
			res.Unchanged = append(res.Unchanged, c.Ref.Code)
		}
	}
	return res, nil
}

// ensureStages creates the stages a repository defines that the server does not have yet.
func (s *Service) ensureStages(ctx context.Context, stages []resource.Stage) error {
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		for _, st := range stages {
			if _, err := ensureStage(ctx, tx, st.ID, st.Name, s.millis()); err != nil {
				return err
			}
		}
		return nil
	})
}

// PutDefinition registers a resource of a one-document kind on the web, or replaces its draft:
// the document is written as it would be in a repository (meta, then the definition) and read
// and checked like an import. A definition with a secret in plain text is refused and not
// stored (ErrPlaintextSecret): it would put the secret into the database.
func (s *Service) PutDefinition(ctx context.Context, a access.Actor, code, kindID string, meta resource.Meta, def map[string]any, ifMatch *string) (DraftResult, error) {
	k, ok := s.lookupKind(kindID)
	if !ok {
		return DraftResult{}, invalid(FieldError{Field: "kind", Code: "unknown"})
	}
	if !k.Layout().File {
		return DraftResult{}, invalid(FieldError{Field: "kind", Code: "not_a_document"})
	}
	if !resource.ValidCode(code) {
		return DraftResult{}, invalid(FieldError{Field: "code", Code: "pattern"})
	}
	data, err := definitionFile(meta, def)
	if err != nil {
		return DraftResult{}, invalid(FieldError{Field: "definition", Code: "invalid", Detail: err.Error()})
	}
	es := []entry{{name: code + k.Layout().Ext, typ: tar.TypeReg, mode: 0o644, data: data}}
	c, err := s.single(ctx, k, code, es, secretNames(meta.Secrets, data))
	if err != nil {
		return DraftResult{}, err
	}
	for _, f := range c.Report.Findings {
		if f.Rule == secretsRule && f.Severity == extcheck.MustFix {
			return DraftResult{}, ErrPlaintextSecret.with(map[string]any{"line": f.Line})
		}
	}
	return s.saveDraft(ctx, a, draftInput{c: c, origin: OriginWeb, ifMatch: ifMatch, checkIfMatch: true})
}

// Upload replaces a resource's draft with the files of an archive (zip, tar or tar.gz, told
// apart by their first bytes), creating the resource when kindID is given and it does not exist.
// The archive is unpacked under the same path safety rules as an import (ErrUnsafePath) and
// the resource read and checked like an import. The caller bounds the size of data.
func (s *Service) Upload(ctx context.Context, a access.Actor, code, kindID string, data []byte, ifMatch *string) (DraftResult, error) {
	if !resource.ValidCode(code) {
		return DraftResult{}, invalid(FieldError{Field: "code", Code: "pattern"})
	}
	if kindID == "" {
		if r, found, err := s.findRow(ctx, s.db, code); err != nil {
			return DraftResult{}, err
		} else if found {
			kindID = r.Kind
		} else {
			return DraftResult{}, invalid(FieldError{Field: "kind", Code: "required"})
		}
	}
	k, ok := s.lookupKind(kindID)
	if !ok {
		return DraftResult{}, invalid(FieldError{Field: "kind", Code: "unknown"})
	}
	if k.Layout().File {
		return DraftResult{}, invalid(FieldError{Field: "kind", Code: "not_a_directory"})
	}
	es, err := readArchive(data)
	if err != nil {
		return DraftResult{}, err
	}
	es = stripRoot(es)
	var contents [][]byte
	for _, e := range es {
		contents = append(contents, e.data)
	}
	c, err := s.single(ctx, k, code, es, secretNames(nil, contents...))
	if err != nil {
		return DraftResult{}, err
	}
	return s.saveDraft(ctx, a, draftInput{c: c, origin: OriginUpload, ifMatch: ifMatch, checkIfMatch: true})
}

// secretsRule is the rule whose findings can be waived: a suspected secret can be a documented
// example. Every other rule is about something that is there or not.
const secretsRule = "secrets"

// single reads and checks one resource given as archive members, in a repository made for it
// (syntheticConfig), with the published catalog as its peers. A resource the reader refuses is
// returned as an error naming every reason.
func (s *Service) single(ctx context.Context, k kind.Kind, code string, es []entry, secrets []string) (candidate, error) {
	stages, err := knownStages(ctx, s.db)
	if err != nil {
		return candidate{}, err
	}
	productIDs, err := s.productIDs(ctx)
	if err != nil {
		return candidate{}, err
	}
	products := slices.Sorted(maps.Keys(productIDs))
	dir, want := k.Layout().Dir+"/"+code, k.Layout().Dir+"/"+code
	if k.Layout().File {
		dir, want = k.Layout().Dir, k.Layout().Dir+"/"+code+k.Layout().Ext
	}
	tree, err := writeTree(syntheticConfig(stages, products, secrets), dir, es)
	if err != nil {
		var ce *Error
		if errors.As(err, &ce) {
			return candidate{}, err
		}
		return candidate{}, fmt.Errorf("catalog: write the upload as a tree: %w", err)
	}
	files, err := bundle.ReadTree(bytes.NewReader(tree))
	if err != nil {
		return candidate{}, unsafe("", string(bundle.RuleNotRelative), err.Error())
	}
	repo, err := repoformat.Read(files, repoformat.Options{Kinds: s.kinds, Version: s.ver})
	if err != nil {
		return candidate{}, fmt.Errorf("catalog: read an upload: %w", err) // the configuration is ours
	}
	peers, err := s.publishedPeers(ctx)
	if err != nil {
		return candidate{}, err
	}
	cs, err := s.checkRepo(ctx, repo, peers)
	if err != nil {
		return candidate{}, err
	}
	i := slices.IndexFunc(cs, func(c candidate) bool { return c.Path == want })
	if i < 0 {
		return candidate{}, invalid(FieldError{Field: "body", Code: "empty", Detail: "no resource in the upload"})
	}
	c := cs[i]
	if !c.Refused {
		return c, nil
	}
	e := &Error{Kind: KindInvalid, Code: ErrInvalid.Code}
	for _, is := range c.Issues {
		if is.Code == repoformat.CodeUnsafePath {
			e.Code, e.Params = ErrUnsafePath.Code, map[string]any{"rule": is.Field}
		}
		e.Fields = append(e.Fields, FieldError{Field: relPath(is.File, want), Code: is.Code, Detail: is.Message})
	}
	return candidate{}, e
}

// relPath is a repository path relative to the resource at base.
func relPath(p, base string) string {
	if p == base {
		return ""
	}
	return strings.TrimPrefix(p, base+"/")
}

// publishedPeers describes the published resources, for checks that compare a resource that
// comes alone (an upload) with what people already have.
func (s *Service) publishedPeers(ctx context.Context) (extcheck.Peers, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, code, COALESCE(current_version, 0), meta FROM resources WHERE status = 'published'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out extcheck.Peers
	for rows.Next() {
		var p extcheck.Peer
		var meta string
		if err := rows.Scan(&p.Ref.Kind, &p.Ref.Code, &p.Ref.Version, &meta); err != nil {
			return nil, err
		}
		var m Meta
		if err := json.Unmarshal([]byte(meta), &m); err != nil {
			return nil, err
		}
		p.Meta, p.Description = m.Meta, m.Description
		out = append(out, p)
	}
	return out, rows.Err()
}
