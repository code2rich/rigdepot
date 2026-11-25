// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/bundle"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// The SQL of the catalog's tables. Only this package reads and writes them.

const resourceCols = `r.id, r.code, r.kind, r.origin, COALESCE(r.source_id, ''), r.source_path, r.scope_kind,
	COALESCE(r.scope_product_id, ''), COALESCE(r.owner_user_id, ''), r.policy, r.audience, r.status,
	COALESCE(r.current_version, 0), r.name, r.summary, r.overrides, r.override_base, r.meta, r.disabled_reason,
	r.revision, r.created_at, r.updated_at`

type resourceRow struct {
	Resource
	overrideBase map[string]json.RawMessage
}

func scanResource(sc interface{ Scan(...any) error }) (resourceRow, error) {
	var (
		r                                  resourceRow
		origin, scopeKind, status          string
		policy, audience                   string
		overrides, base, meta              string
		created, updated                   int64
		sourceID, productID, ownerID, path string
	)
	err := sc.Scan(&r.ID, &r.Code, &r.Kind, &origin, &sourceID, &path, &scopeKind, &productID, &ownerID, &policy, &audience,
		&status, &r.Version, &r.Name, &r.Summary, &overrides, &base, &meta, &r.DisabledReason, &r.Revision, &created, &updated)
	if err != nil {
		return r, err
	}
	r.Origin, r.Status = Origin(origin), Status(status)
	r.SourceID, r.SourcePath, r.ProductID, r.OwnerID = sourceID, path, productID, ownerID
	r.Policy, r.Audience = resource.Policy(policy), resource.Audience(audience)
	r.CreatedAt, r.UpdatedAt = dbx.Time(created), dbx.Time(updated)
	if err := json.Unmarshal([]byte(overrides), &r.Overrides); err != nil {
		return r, fmt.Errorf("catalog: overrides of %s: %w", r.Code, err)
	}
	if err := json.Unmarshal([]byte(base), &r.overrideBase); err != nil {
		return r, fmt.Errorf("catalog: override_base of %s: %w", r.Code, err)
	}
	if err := json.Unmarshal([]byte(meta), &r.Meta); err != nil {
		return r, fmt.Errorf("catalog: meta of %s: %w", r.Code, err)
	}
	r.Scope = r.Meta.Scope
	if r.Scope == "" {
		r.Scope = resource.Scope(scopeKind)
	}
	r.Overrides = sortedFields(r.Overrides)
	return r, nil
}

// loadRows reads the resources matching where (a condition on r) with their stages,
// maintainers, references, drafts and the findings that decide Checks and Blocked.
func (s *Service) loadRows(ctx context.Context, q dbx.Querier, where string, args ...any) ([]resourceRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+resourceCols+` FROM resources r WHERE `+where+` ORDER BY r.name, r.code`, args...)
	if err != nil {
		return nil, err
	}
	var out []resourceRow
	byID := map[string]int{}
	for rows.Next() {
		r, err := scanResource(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		byID[r.ID] = len(out)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	ids := make([]any, 0, len(out))
	for _, r := range out {
		ids = append(ids, r.ID)
	}
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
	each := func(query string, fn func(sc *sql.Rows) error) error {
		rows, err := q.QueryContext(ctx, query, ids...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := fn(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	// Stages, by position.
	if err := each(`SELECT rs.resource_id, st.code FROM resource_stages rs JOIN stages st ON st.id = rs.stage_id
		WHERE rs.resource_id IN `+in+` ORDER BY st.position, st.code`, func(sc *sql.Rows) error {
		var id, code string
		if err := sc.Scan(&id, &code); err != nil {
			return err
		}
		r := &out[byID[id]]
		r.Stages = append(r.Stages, code)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := each(`SELECT resource_id, user_id FROM resource_maintainers WHERE resource_id IN `+in+` ORDER BY user_id`, func(sc *sql.Rows) error {
		var id, user string
		if err := sc.Scan(&id, &user); err != nil {
			return err
		}
		r := &out[byID[id]]
		r.Maintainers = append(r.Maintainers, user)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := each(`SELECT resource_id, ref_code FROM resource_refs WHERE resource_id IN `+in+` ORDER BY ref_code`, func(sc *sql.Rows) error {
		var id, code string
		if err := sc.Scan(&id, &code); err != nil {
			return err
		}
		r := &out[byID[id]]
		r.References = append(r.References, code)
		return nil
	}); err != nil {
		return nil, err
	}
	waivers := map[string][]Waiver{}
	if err := each(`SELECT `+waiverCols+` FROM check_waivers WHERE resource_id IN `+in, func(sc *sql.Rows) error {
		id, w, err := scanWaiver(sc)
		waivers[id] = append(waivers[id], w)
		return err
	}); err != nil {
		return nil, err
	}
	// The draft decides Checks when there is one; Blocked looks at both.
	if err := each(`SELECT resource_id, findings, source_commit FROM resource_drafts WHERE resource_id IN `+in, func(sc *sql.Rows) error {
		var id, findings, commit string
		if err := sc.Scan(&id, &findings, &commit); err != nil {
			return err
		}
		r := &out[byID[id]]
		rep, err := decodeReport(0, findings)
		if err != nil {
			return fmt.Errorf("catalog: findings of the draft of %s: %w", r.Code, err)
		}
		applyWaivers(&rep, waivers[id])
		r.HasDraft, r.DraftCommit, r.Checks = true, commit, rep.Counts()
		r.Blocked = r.Checks.Blocking > 0
		return nil
	}); err != nil {
		return nil, err
	}
	if err := each(`SELECT v.resource_id, v.findings, v.source_commit, v.published_at FROM resource_versions v JOIN resources r
		ON r.id = v.resource_id AND r.current_version = v.number WHERE v.resource_id IN `+in, func(sc *sql.Rows) error {
		var id, findings, commit string
		var at int64
		if err := sc.Scan(&id, &findings, &commit, &at); err != nil {
			return err
		}
		r := &out[byID[id]]
		rep, err := decodeReport(0, findings)
		if err != nil {
			return fmt.Errorf("catalog: findings of %s v%d: %w", r.Code, r.Version, err)
		}
		applyWaivers(&rep, waivers[id])
		c := rep.Counts()
		if !r.HasDraft {
			r.Checks = c
		}
		r.Blocked = r.Blocked || c.Blocking > 0
		r.LiveCommit, r.PublishedAt = commit, dbx.Time(at)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// loadRow reads one resource; ErrNotFound when there is none.
func (s *Service) loadRow(ctx context.Context, q dbx.Querier, code string) (resourceRow, error) {
	rs, err := s.loadRows(ctx, q, `r.code = ?`, code)
	if err != nil {
		return resourceRow{}, err
	}
	if len(rs) == 0 {
		return resourceRow{}, ErrNotFound
	}
	return rs[0], nil
}

// findRow is loadRow that returns false instead of ErrNotFound.
func (s *Service) findRow(ctx context.Context, q dbx.Querier, code string) (resourceRow, bool, error) {
	r, err := s.loadRow(ctx, q, code)
	if errors.Is(err, ErrNotFound) {
		return r, false, nil
	}
	return r, err == nil, err
}

const waiverCols = `resource_id, id, rule, file, line_digest, reason, created_by, created_at`

func scanWaiver(sc interface{ Scan(...any) error }) (string, Waiver, error) {
	var resourceID string
	var w Waiver
	var at int64
	err := sc.Scan(&resourceID, &w.ID, &w.Rule, &w.File, &w.FindingID, &w.Reason, &w.By, &at)
	w.At = dbx.Time(at)
	return resourceID, w, err
}

func (s *Service) waivers(ctx context.Context, q dbx.Querier, resourceID string) ([]Waiver, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+waiverCols+` FROM check_waivers WHERE resource_id = ? ORDER BY created_at`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Waiver
	for rows.Next() {
		_, w, err := scanWaiver(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// applyWaivers marks the findings a waiver covers: the same rule, the same file and the same
// identifier, which follows the content of the line. A changed line no longer matches.
func applyWaivers(r *Report, ws []Waiver) {
	for i := range r.Findings {
		f := &r.Findings[i]
		f.Waiver = nil
		for j := range ws {
			if ws[j].Rule == f.Rule && ws[j].File == f.File && ws[j].FindingID == f.ID {
				f.Waiver = &ws[j]
				break
			}
		}
	}
}

func decodeReport(ruleset int, findings string) (Report, error) {
	r := Report{Ruleset: ruleset, Findings: []Finding{}}
	if err := json.Unmarshal([]byte(findings), &r.Findings); err != nil {
		return r, err
	}
	return r, nil
}

func encodeJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("catalog: encode %T: %v", v, err)) // only types of this package, which always encode
	}
	return string(b)
}

const draftCols = `d.digest, d.bundle, d.meta, d.ruleset, d.findings, d.source_commit, COALESCE(d.updated_by, ''), d.updated_at`

// draft reads the draft of a resource; false when it has none.
func (s *Service) draft(ctx context.Context, q dbx.Querier, r Resource) (Draft, bool, error) {
	d := Draft{Code: r.Code, Kind: r.Kind, BaseVersion: r.Version}
	var meta, findings string
	var ruleset int
	var at int64
	err := q.QueryRowContext(ctx, `SELECT `+draftCols+` FROM resource_drafts d WHERE d.resource_id = ?`, r.ID).
		Scan(&d.Digest, &d.Bundle, &meta, &ruleset, &findings, &d.Commit, &d.UpdatedBy, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	d.UpdatedAt = dbx.Time(at)
	if err := json.Unmarshal([]byte(meta), &d.Meta); err != nil {
		return d, false, fmt.Errorf("catalog: meta of the draft of %s: %w", r.Code, err)
	}
	if d.Checks, err = decodeReport(ruleset, findings); err != nil {
		return d, false, fmt.Errorf("catalog: findings of the draft of %s: %w", r.Code, err)
	}
	ws, err := s.waivers(ctx, q, r.ID)
	if err != nil {
		return d, false, err
	}
	applyWaivers(&d.Checks, ws)
	return d, true, nil
}

const versionCols = `number, digest, bundle, meta, ruleset, findings, source_commit, COALESCE(rollback_of, 0), note, published_by, published_at`

func scanVersion(sc interface{ Scan(...any) error }) (Version, error) {
	var v Version
	var meta, findings string
	var ruleset int
	var at int64
	if err := sc.Scan(&v.Number, &v.Digest, &v.Bundle, &meta, &ruleset, &findings, &v.Commit, &v.RollbackOf, &v.Note, &v.PublishedBy, &at); err != nil {
		return v, err
	}
	v.PublishedAt = dbx.Time(at)
	if err := json.Unmarshal([]byte(meta), &v.Meta); err != nil {
		return v, fmt.Errorf("catalog: meta of v%d: %w", v.Number, err)
	}
	var err error
	v.Checks, err = decodeReport(ruleset, findings)
	return v, err
}

// version reads one version; ErrNotFound when the resource has no such version.
func (s *Service) version(ctx context.Context, q dbx.Querier, resourceID string, n int) (Version, error) {
	v, err := scanVersion(q.QueryRowContext(ctx, `SELECT `+versionCols+` FROM resource_versions WHERE resource_id = ? AND number = ?`, resourceID, n))
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	ws, err := s.waivers(ctx, q, resourceID)
	if err != nil {
		return v, err
	}
	applyWaivers(&v.Checks, ws)
	return v, nil
}

// putBlob stores a bundle under its address; storing the same bytes again changes nothing.
func putBlob(ctx context.Context, tx *dbx.Tx, p bundle.Packed, now int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO blobs (digest, size, data, created_at) VALUES (?, ?, ?, ?) ON CONFLICT (digest) DO NOTHING`,
		p.Address, len(p.Data), p.Data, now)
	return err
}

// blob reads a bundle by its address; ErrNotFound when there is none.
func blob(ctx context.Context, q dbx.Querier, address string) ([]byte, error) {
	var data []byte
	err := q.QueryRowContext(ctx, `SELECT data FROM blobs WHERE digest = ?`, address).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return data, err
}

// files unpacks a stored bundle.
func files(ctx context.Context, q dbx.Querier, address string) (*bundle.Files, error) {
	data, err := blob(ctx, q, address)
	if err != nil {
		return nil, err
	}
	f, err := bundle.Unpack(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("catalog: bundle %s: %w", address, err)
	}
	return f, nil
}

// fileList lists the files of unpacked content with their sizes.
func fileList(f *bundle.Files) []File {
	out := []File{}
	for _, p := range f.Paths() {
		data, err := f.ReadFile(p)
		if err != nil {
			continue
		}
		out = append(out, File{Path: p, Size: len(data)})
	}
	return out
}

// setStages replaces the stages of a resource, creating the stage rows that do not exist yet
// (a repository may name stages the server has not seen).
func setStages(ctx context.Context, tx *dbx.Tx, resourceID string, codes []string, now int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM resource_stages WHERE resource_id = ?`, resourceID); err != nil {
		return err
	}
	for _, code := range dedupe(codes) {
		id, err := ensureStage(ctx, tx, code, "", now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource_stages (resource_id, stage_id) VALUES (?, ?)`, resourceID, id); err != nil {
			return err
		}
	}
	return nil
}

// ensureStage returns the id of the stage with the code, creating it when it does not exist.
// The default stages keep their order; others go after them.
func ensureStage(ctx context.Context, tx *dbx.Tx, code, name string, now int64) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM stages WHERE code = ?`, code).Scan(&id)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return id, err
	}
	pos := 100
	for i, st := range resource.DefaultStages() {
		if st.ID == code {
			pos = i
		}
	}
	id = dbx.NewID()
	_, err = tx.ExecContext(ctx, `INSERT INTO stages (id, code, name, position, hidden, created_at, updated_at) VALUES (?, ?, ?, ?, FALSE, ?, ?)`,
		id, code, name, pos, now, now)
	return id, err
}

// knownStages returns the codes of the stages the server has and the default ones.
func knownStages(ctx context.Context, q dbx.Querier) ([]resource.Stage, error) {
	out := resource.DefaultStages()
	rows, err := q.QueryContext(ctx, `SELECT code, name, hidden FROM stages ORDER BY position, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st resource.Stage
		if err := rows.Scan(&st.ID, &st.Name, &st.Hidden); err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(out, func(x resource.Stage) bool { return x.ID == st.ID }) {
			out = append(out, st)
		}
	}
	return out, rows.Err()
}

func setMaintainers(ctx context.Context, tx *dbx.Tx, resourceID string, users []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM resource_maintainers WHERE resource_id = ?`, resourceID); err != nil {
		return err
	}
	for _, u := range dedupe(users) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource_maintainers (resource_id, user_id) VALUES (?, ?)`, resourceID, u); err != nil {
			return err
		}
	}
	return nil
}

func setRefs(ctx context.Context, tx *dbx.Tx, resourceID string, codes []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM resource_refs WHERE resource_id = ?`, resourceID); err != nil {
		return err
	}
	for _, c := range dedupe(codes) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource_refs (resource_id, ref_code) VALUES (?, ?)`, resourceID, c); err != nil {
			return err
		}
	}
	return nil
}

func dedupe(xs []string) []string {
	out := []string{}
	for _, x := range xs {
		if x != "" && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}
