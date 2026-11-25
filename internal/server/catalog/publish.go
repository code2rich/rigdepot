// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"unicode/utf8"

	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/events"
)

// lockRow takes the resource's lock for the rest of the transaction (version numbers are read
// and written in it) and reads it.
func (s *Service) lockRow(o *op, code string) (resourceRow, error) {
	if err := o.tx.Lock(o.ctx, "catalog.resource:"+code); err != nil {
		return resourceRow{}, err
	}
	return s.loadRow(o.ctx, o.tx, code)
}

// matchResource checks If-Match against the resource's ETag.
func matchResource(r Resource, ifMatch string) error {
	if ifMatch == "" {
		return ErrPreconditionRequired
	}
	if ifMatch != r.ETag() {
		return ErrPreconditionFailed
	}
	return nil
}

// Publish publishes the draft as a new version, in one transaction: the audit entry, the new
// version (versions are only ever inserted), the live pointer and the settings from the draft's
// metadata (except platform overrides), and the draft cleared. ifMatch is the draft's ETag the
// person saw: a draft that changed since is ErrPreconditionFailed and nothing is published.
// A must-fix finding that is not waived refuses it (ErrCheckBlocked).
func (s *Service) Publish(ctx context.Context, a access.Actor, code, ifMatch, note string) (Version, Resource, error) {
	if utf8.RuneCountInString(note) > 500 {
		return Version{}, Resource{}, invalid(FieldError{Field: "note", Code: "too_long"})
	}
	productIDs, err := s.productIDs(ctx)
	if err != nil {
		return Version{}, Resource{}, err
	}
	var v Version
	var out Resource
	err = s.write(ctx, a, func(o *op) error {
		r, err := s.lockRow(o, code)
		if err != nil {
			return err
		}
		obj := resourceObject(r.Code, r.Name)
		if err := o.check(access.ActionResourcePublish, r.target(), obj); err != nil {
			return err
		}
		d, has, err := s.draft(ctx, o.tx, r.Resource)
		if err != nil {
			return err
		}
		if !has {
			return ErrNoDraft
		}
		if ifMatch == "" {
			return ErrPreconditionRequired
		}
		if ifMatch != d.ETag() {
			return ErrPreconditionFailed
		}
		if n := d.Checks.Counts().Blocking; n > 0 {
			return ErrCheckBlocked.with(map[string]any{"blocking": n})
		}
		v = Version{Digest: d.Digest, Bundle: d.Bundle, Meta: d.Meta, Checks: d.Checks, Commit: d.Commit, Note: note}
		return s.insertVersion(o, r, &v, productIDs, map[string]any{"from": r.Version}, &out)
	})
	return v, out, err
}

// insertVersion publishes v as the next version of r: the row, the live pointer, the settings
// from its metadata, its references, the draft cleared (unless it is a rollback, which leaves
// the draft waiting), the audit entry and the event.
func (s *Service) insertVersion(o *op, r resourceRow, v *Version, productIDs map[string]string, change map[string]any, out *Resource) error {
	ctx, now := o.ctx, s.millis()
	if o.actor.UserID == "" {
		return ErrDenied // a version is published by a person
	}
	if err := o.tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number), 0) + 1 FROM resource_versions WHERE resource_id = ?`, r.ID).Scan(&v.Number); err != nil {
		return err
	}
	var rollbackOf any
	if v.RollbackOf > 0 {
		rollbackOf = v.RollbackOf
	}
	v.PublishedBy, v.PublishedAt = o.actor.UserID, dbx.Time(now)
	if _, err := o.tx.ExecContext(ctx, `INSERT INTO resource_versions (resource_id, number, digest, bundle, meta, ruleset, findings,
		source_commit, rollback_of, note, published_by, published_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, v.Number, string(v.Digest), v.Bundle, encodeJSON(v.Meta), v.Checks.Ruleset, encodeJSON(v.Checks.Findings),
		v.Commit, rollbackOf, v.Note, v.PublishedBy, now); err != nil {
		return err
	}
	if v.RollbackOf == 0 {
		if _, err := o.tx.ExecContext(ctx, `DELETE FROM resource_drafts WHERE resource_id = ?`, r.ID); err != nil {
			return err
		}
	}
	status := StatusPublished
	if r.Status == StatusDisabled {
		status = StatusDisabled // publishing does not bring a disabled resource back; enabling does
	}
	if _, err := o.tx.ExecContext(ctx, `UPDATE resources SET current_version = ?, status = ? WHERE id = ?`, v.Number, string(status), r.ID); err != nil {
		return err
	}
	r, err := s.applyContent(o, r, v.Meta, productIDs)
	if err != nil {
		return err
	}
	if err := setRefs(ctx, o.tx, r.ID, v.Meta.References); err != nil {
		return err
	}
	if err := s.touch(o, r.ID, now); err != nil {
		return err
	}
	action := access.ActionResourcePublish
	change["to"] = v.Number
	if v.RollbackOf > 0 {
		action = access.ActionResourceRollback
		change["rollback_of"] = v.RollbackOf
	}
	if v.Note != "" {
		change["note"] = v.Note
	}
	if err := o.record(action, resourceObject(r.Code, r.Name), change); err != nil {
		return err
	}
	updated, err := s.loadRow(ctx, o.tx, r.Code)
	if err != nil {
		return err
	}
	*out = updated.Resource
	o.emit(events.ResourcePublished{ResourceRef: updated.ref(), Version: v.Number, Previous: r.Version, RollbackOf: v.RollbackOf, ActorID: o.actor.UserID})
	return nil
}

// Rollback publishes an earlier version's content again as a new version whose RollbackOf names
// it: history is never rewritten. ifMatch is the resource's ETag. A draft waiting stays.
func (s *Service) Rollback(ctx context.Context, a access.Actor, code, ifMatch string, to int, note string) (Version, Resource, error) {
	if utf8.RuneCountInString(note) > 500 {
		return Version{}, Resource{}, invalid(FieldError{Field: "note", Code: "too_long"})
	}
	productIDs, err := s.productIDs(ctx)
	if err != nil {
		return Version{}, Resource{}, err
	}
	var v Version
	var out Resource
	err = s.write(ctx, a, func(o *op) error {
		r, err := s.lockRow(o, code)
		if err != nil {
			return err
		}
		if err := o.check(access.ActionResourceRollback, r.target(), resourceObject(r.Code, r.Name)); err != nil {
			return err
		}
		if err := matchResource(r.Resource, ifMatch); err != nil {
			return err
		}
		if to == r.Version {
			return invalid(FieldError{Field: "to_version", Code: "current"})
		}
		old, err := s.version(ctx, o.tx, r.ID, to)
		if errors.Is(err, ErrNotFound) {
			return invalid(FieldError{Field: "to_version", Code: "unknown"})
		}
		if err != nil {
			return err
		}
		old.Checks.Findings = slices.Clone(old.Checks.Findings)
		v = Version{Digest: old.Digest, Bundle: old.Bundle, Meta: old.Meta, Checks: old.Checks, Commit: old.Commit, RollbackOf: old.Number, Note: note}
		return s.insertVersion(o, r, &v, productIDs, map[string]any{"from": r.Version}, &out)
	})
	return v, out, err
}

// DiscardDraft throws a draft away; the live version stays. A resource that was never
// published disappears with its draft. ifMatch is the draft's ETag.
func (s *Service) DiscardDraft(ctx context.Context, a access.Actor, code, ifMatch string) error {
	return s.write(ctx, a, func(o *op) error {
		r, err := s.lockRow(o, code)
		if err != nil {
			return err
		}
		obj := resourceObject(r.Code, r.Name)
		if err := o.check(access.ActionResourceEdit, r.target(), obj); err != nil {
			return err
		}
		d, has, err := s.draft(ctx, o.tx, r.Resource)
		if err != nil {
			return err
		}
		if !has {
			return ErrNoDraft
		}
		if ifMatch == "" {
			return ErrPreconditionRequired
		}
		if ifMatch != d.ETag() {
			return ErrPreconditionFailed
		}
		if _, err := o.tx.ExecContext(ctx, `DELETE FROM resource_drafts WHERE resource_id = ?`, r.ID); err != nil {
			return err
		}
		change := map[string]any{"draft": "discarded", "digest": string(d.Digest)}
		if r.Version == 0 {
			for _, q := range []string{
				`DELETE FROM resource_stages WHERE resource_id = ?`, `DELETE FROM resource_maintainers WHERE resource_id = ?`,
				`DELETE FROM resource_refs WHERE resource_id = ?`, `DELETE FROM check_waivers WHERE resource_id = ?`,
				`DELETE FROM resources WHERE id = ?`,
			} {
				if _, err := o.tx.ExecContext(ctx, q, r.ID); err != nil {
					return err
				}
			}
			change["resource"] = "removed"
		} else if err := s.touch(o, r.ID, s.millis()); err != nil {
			return err
		}
		return o.record(access.ActionResourceEdit, obj, change)
	})
}

// Disable takes a resource off every computer at their next sync. Versions and history stay.
func (s *Service) Disable(ctx context.Context, a access.Actor, code, ifMatch, reason string) (Resource, error) {
	if reason == "" {
		return Resource{}, invalid(FieldError{Field: "reason", Code: "required"})
	}
	if utf8.RuneCountInString(reason) > 500 {
		return Resource{}, invalid(FieldError{Field: "reason", Code: "too_long"})
	}
	return s.setStatus(ctx, a, code, ifMatch, access.ActionResourceDisable, reason)
}

// Enable brings a disabled resource back at its last version (or as a draft, if it was never
// published).
func (s *Service) Enable(ctx context.Context, a access.Actor, code, ifMatch string) (Resource, error) {
	return s.setStatus(ctx, a, code, ifMatch, access.ActionResourceEnable, "")
}

func (s *Service) setStatus(ctx context.Context, a access.Actor, code, ifMatch string, action access.Action, reason string) (Resource, error) {
	var out Resource
	err := s.write(ctx, a, func(o *op) error {
		r, err := s.lockRow(o, code)
		if err != nil {
			return err
		}
		obj := resourceObject(r.Code, r.Name)
		if err := o.check(action, r.target(), obj); err != nil {
			return err
		}
		if err := matchResource(r.Resource, ifMatch); err != nil {
			return err
		}
		status := StatusDisabled
		if action == access.ActionResourceEnable {
			status = StatusDraft
			if r.Version > 0 {
				status = StatusPublished
			}
		}
		if status == r.Status {
			out = r.Resource
			return nil
		}
		if _, err := o.tx.ExecContext(ctx, `UPDATE resources SET status = ?, disabled_reason = ? WHERE id = ?`, string(status), reason, r.ID); err != nil {
			return err
		}
		if err := s.touch(o, r.ID, s.millis()); err != nil {
			return err
		}
		change := map[string]any{"status": map[string]any{"from": string(r.Status), "to": string(status)}}
		if reason != "" {
			change["reason"] = reason
		}
		if err := o.record(action, obj, change); err != nil {
			return err
		}
		updated, err := s.loadRow(ctx, o.tx, code)
		if err != nil {
			return err
		}
		out = updated.Resource
		if status == StatusDisabled {
			o.emit(events.ResourceDisabled{ResourceRef: updated.ref(), ActorID: a.UserID})
		} else {
			o.emit(events.ResourceEnabled{ResourceRef: updated.ref(), ActorID: a.UserID})
		}
		return nil
	})
	return out, err
}

// Change is a change of who gets a resource and how, made on the platform. Nil fields stay.
type Change struct {
	Name        *string
	Summary     *string
	Stages      *[]string
	Audience    *resource.Audience
	Scope       *resource.Scope
	Policy      *resource.Policy
	When        *string
	Try         *string
	Maintainers *[]string // user ids
	Delivery    *Delivery
	// Reset drops these overrides: the fields take the content's values again.
	Reset []string
}

// Configure changes who gets a resource and how. It takes effect at once, without a new version:
// the content does not change. Every field it sets becomes a platform override, so that later
// changes of that field in the repository are reported (RepositoryDiffers) but not applied,
// until the override is dropped with Reset. ifMatch is the resource's ETag.
func (s *Service) Configure(ctx context.Context, a access.Actor, code, ifMatch string, c Change) (Resource, error) {
	productIDs, err := s.productIDs(ctx)
	if err != nil {
		return Resource{}, err
	}
	if err := s.validateChange(ctx, c, productIDs); err != nil {
		return Resource{}, err
	}
	var out Resource
	err = s.write(ctx, a, func(o *op) error {
		r, err := s.lockRow(o, code)
		if err != nil {
			return err
		}
		obj := resourceObject(r.Code, r.Name)
		if err := o.check(access.ActionResourceConfigure, r.target(), obj); err != nil {
			return err
		}
		if err := matchResource(r.Resource, ifMatch); err != nil {
			return err
		}
		content, err := s.contentMeta(ctx, o.tx, r.Resource, false)
		if err != nil {
			return err
		}
		if c.Scope != nil && (*c.Scope == resource.ScopeMe) != (r.Scope == resource.ScopeMe) {
			// Sharing a personal resource is a request others approve (M2); making a shared one
			// personal would give it to nobody.
			return invalid(FieldError{Field: "scope", Code: "personal"})
		}
		eff := r.Meta
		fields := map[string]any{}
		over := slices.Clone(r.Overrides)
		mark := func(f string, from, to any, contentValue any) {
			fields[f] = map[string]any{"from": from, "to": to}
			if !slices.Contains(over, f) {
				over = append(over, f)
				r.overrideBase[f] = json.RawMessage(encodeJSON(contentValue))
			}
		}
		var maintainers []string
		if c.Name != nil {
			mark(FieldName, eff.Name, *c.Name, content.Name)
			eff.Name = *c.Name
		}
		if c.Summary != nil {
			mark(FieldSummary, eff.Summary, *c.Summary, content.Summary)
			eff.Summary = *c.Summary
		}
		if c.Stages != nil {
			mark(FieldStages, eff.Stages, *c.Stages, content.Stages)
			eff.Stages = dedupe(*c.Stages)
		}
		if c.Audience != nil {
			mark(FieldAudience, eff.Audience, *c.Audience, content.Audience)
			eff.Audience = *c.Audience
		}
		if c.Scope != nil {
			mark(FieldScope, eff.Scope, *c.Scope, content.Scope)
			eff.Scope = *c.Scope
		}
		if c.Policy != nil {
			mark(FieldPolicy, eff.Policy, *c.Policy, content.Policy)
			eff.Policy = *c.Policy
		}
		if c.When != nil {
			mark(FieldWhen, eff.When, *c.When, content.When)
			eff.When = *c.When
		}
		if c.Try != nil {
			mark(FieldTry, eff.Try, *c.Try, content.Try)
			eff.Try = *c.Try
		}
		if c.Delivery != nil {
			mark(FieldDelivery, eff.Delivery, *c.Delivery, content.Delivery)
			eff.Delivery = *c.Delivery
		}
		if c.Maintainers != nil {
			mark(FieldMaintainers, r.Maintainers, *c.Maintainers, content.Maintainers)
			maintainers = dedupe(*c.Maintainers)
		}
		// Reset: back to the content's values.
		var reset []string
		for _, f := range c.Reset {
			if !slices.Contains(over, f) {
				continue
			}
			over = slices.DeleteFunc(over, func(x string) bool { return x == f })
			delete(r.overrideBase, f)
			reset = append(reset, f)
		}
		r.Overrides, r.Meta = over, eff
		if _, err := o.tx.ExecContext(ctx, `UPDATE resources SET overrides = ?, override_base = ? WHERE id = ?`,
			encodeJSON(sortedFields(over)), encodeJSON(r.overrideBase), r.ID); err != nil {
			return err
		}
		if len(reset) > 0 {
			// applyContent takes every field that is not overridden from the content.
			if r, err = s.applyContent(o, r, content, productIDs); err != nil {
				return err
			}
		} else if r, err = s.storeSettings(o, r, eff, productIDs); err != nil {
			return err
		}
		if c.Maintainers != nil {
			if err := setMaintainers(ctx, o.tx, r.ID, maintainers); err != nil {
				return err
			}
		}
		if len(fields) == 0 && len(reset) == 0 {
			out = r.Resource
			return nil
		}
		if err := s.touch(o, r.ID, s.millis()); err != nil {
			return err
		}
		change := map[string]any{}
		for f, v := range fields {
			change[f] = v
		}
		if len(reset) > 0 {
			change["reset"] = sortedFields(reset)
		}
		if err := o.record(access.ActionResourceConfigure, obj, change); err != nil {
			return err
		}
		updated, err := s.loadRow(ctx, o.tx, code)
		if err != nil {
			return err
		}
		out = s.withDiffers(ctx, o.tx, updated)
		names := append(sortedFields(keys(fields)), reset...)
		o.emit(events.ResourceUpdated{ResourceRef: updated.ref(), Fields: sortedFields(names), ActorID: a.UserID})
		return nil
	})
	return out, err
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (s *Service) validateChange(ctx context.Context, c Change, productIDs map[string]string) error {
	var fs []FieldError
	text := func(field string, v *string, minLen, maxLen int) {
		if v != nil {
			n := utf8.RuneCountInString(*v)
			if n < minLen {
				fs = append(fs, FieldError{Field: field, Code: "required"})
			} else if n > maxLen {
				fs = append(fs, FieldError{Field: field, Code: "too_long"})
			}
		}
	}
	text("name", c.Name, 1, 64)
	text("summary", c.Summary, 0, 500)
	text("when", c.When, 0, 200)
	text("try", c.Try, 0, 200)
	if c.Audience != nil && !c.Audience.Valid() {
		fs = append(fs, FieldError{Field: "audience", Code: "enum"})
	}
	if c.Policy != nil && !c.Policy.Valid() {
		fs = append(fs, FieldError{Field: "policy", Code: "enum"})
	}
	if c.Delivery != nil && *c.Delivery != DeliveryNative && *c.Delivery != DeliveryOnDemand {
		fs = append(fs, FieldError{Field: "delivery", Code: "enum"})
	}
	if c.Scope != nil {
		if code, ok := c.Scope.Product(); ok && productIDs[code] == "" {
			fs = append(fs, FieldError{Field: "scope", Code: "unknown"})
		}
	}
	if c.Stages != nil {
		known, err := knownStages(ctx, s.db)
		if err != nil {
			return err
		}
		for i, st := range *c.Stages {
			if !slices.ContainsFunc(known, func(k resource.Stage) bool { return k.ID == st }) {
				fs = append(fs, FieldError{Field: "stages[" + itoa(i) + "]", Code: "unknown"})
			}
		}
	}
	if c.Maintainers != nil {
		for i, id := range *c.Maintainers {
			if _, err := s.access.GetUser(ctx, access.System, id); err != nil {
				fs = append(fs, FieldError{Field: "maintainers[" + itoa(i) + "]", Code: "unknown"})
			}
		}
	}
	for i, f := range c.Reset {
		if !slices.Contains(overridable, f) {
			fs = append(fs, FieldError{Field: "reset[" + itoa(i) + "]", Code: "enum"})
		}
	}
	if len(fs) > 0 {
		return invalid(fs...)
	}
	return nil
}

// contentMeta is the metadata the content says, before overrides: the draft's when draftFirst
// and there is one (what the repository says now), else the live version's, else the draft's.
func (s *Service) contentMeta(ctx context.Context, q dbx.Querier, r Resource, draftFirst bool) (Meta, error) {
	d, has, err := s.draft(ctx, q, r)
	if err != nil {
		return Meta{}, err
	}
	if has && (draftFirst || r.Version == 0) {
		return d.Meta, nil
	}
	if r.Version > 0 {
		v, err := s.version(ctx, q, r.ID, r.Version)
		return v.Meta, err
	}
	return r.Meta, nil
}

// withDiffers fills in RepositoryDiffers: the overridden fields whose value in the content
// (the newest: the draft's when there is one) is no longer what it was when overridden.
func (s *Service) withDiffers(ctx context.Context, q dbx.Querier, r resourceRow) Resource {
	out := r.Resource
	out.RepositoryDiffers = []string{}
	if len(r.Overrides) == 0 {
		return out
	}
	m, err := s.contentMeta(ctx, q, r.Resource, true)
	if err != nil {
		return out
	}
	for _, f := range r.Overrides {
		base, ok := r.overrideBase[f]
		if ok && string(base) != encodeJSON(fieldValue(m, f)) {
			out.RepositoryDiffers = append(out.RepositoryDiffers, f)
		}
	}
	return out
}

// fieldValue is one overridable field of m, as compared with an override's base.
func fieldValue(m Meta, f string) any {
	switch f {
	case FieldName:
		return m.Name
	case FieldSummary:
		return m.Summary
	case FieldStages:
		return m.Stages
	case FieldAudience:
		return m.Audience
	case FieldScope:
		return m.Scope
	case FieldPolicy:
		return m.Policy
	case FieldWhen:
		return m.When
	case FieldTry:
		return m.Try
	case FieldMaintainers:
		return m.Maintainers
	case FieldDelivery:
		return m.Delivery
	}
	return nil
}

// Waive marks a finding of the draft or the live version as a false positive. Only findings of
// the suspected-secret rule can be waived (ErrNotWaivable). The waiver is tied to the rule, the
// file and the finding's identifier, which follows the line's content: when the content changes,
// it no longer applies.
func (s *Service) Waive(ctx context.Context, a access.Actor, code, findingID, reason string) (Waiver, error) {
	if reason == "" {
		return Waiver{}, invalid(FieldError{Field: "reason", Code: "required"})
	}
	if utf8.RuneCountInString(reason) > 500 {
		return Waiver{}, invalid(FieldError{Field: "reason", Code: "too_long"})
	}
	var w Waiver
	err := s.write(ctx, a, func(o *op) error {
		r, err := s.lockRow(o, code)
		if err != nil {
			return err
		}
		obj := resourceObject(r.Code, r.Name)
		if err := o.check(access.ActionResourceWaive, r.target(), obj); err != nil {
			return err
		}
		var found *Finding
		d, has, err := s.draft(ctx, o.tx, r.Resource)
		if err != nil {
			return err
		}
		reports := []Report{}
		if has {
			reports = append(reports, d.Checks)
		}
		if r.Version > 0 {
			v, err := s.version(ctx, o.tx, r.ID, r.Version)
			if err != nil {
				return err
			}
			reports = append(reports, v.Checks)
		}
		for _, rep := range reports {
			for i := range rep.Findings {
				if rep.Findings[i].ID == findingID {
					found = &rep.Findings[i]
				}
			}
		}
		if found == nil {
			return invalid(FieldError{Field: "finding", Code: "unknown"})
		}
		if found.Rule != secretsRule || found.Severity != extcheck.MustFix {
			return ErrNotWaivable.with(map[string]any{"rule": found.Rule})
		}
		if found.Waiver != nil {
			w = *found.Waiver
			return nil
		}
		now := s.millis()
		w = Waiver{ID: dbx.NewID(), Rule: found.Rule, File: found.File, FindingID: found.ID, Reason: reason, By: a.UserID, At: dbx.Time(now)}
		if _, err := o.tx.ExecContext(ctx, `INSERT INTO check_waivers (id, resource_id, rule, file, line_digest, reason, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, w.ID, r.ID, w.Rule, w.File, w.FindingID, w.Reason, w.By, now); err != nil {
			return err
		}
		if err := s.touch(o, r.ID, now); err != nil {
			return err
		}
		return o.record(access.ActionResourceWaive, obj, map[string]any{"finding": found.ID, "rule": found.Rule, "file": found.File, "reason": reason})
	})
	return w, err
}

func itoa(i int) string { return strconv.Itoa(i) }
