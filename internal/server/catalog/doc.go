// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package catalog manages resources, drafts, versions, publish, rollback and retire, references
// and impact, and source imports.
//
// # Content comes in as drafts
//
// Content reaches the catalog three ways, and all three end in the same place: an import from
// a resource repository (ImportRepo, run by the source's pull job), an archive uploaded on the
// web or with rgd (Upload), and a one-document resource registered on the web (PutDefinition).
// An upload and a registration are written out as a repository of one resource and read by
// the same reader (internal/repoformat on bundle.ReadTree), so the path safety rules, the
// schemas and the checks are exactly an import's. Every resource is checked like rgd check
// does (internal/check: FromRepo, Refused, FindingIDs; the reader's notices as advisory
// findings), with the same rule set, and the result is stored with the draft.
//
// A draft never reaches anyone's computer. Publishing is a separate, explicit request by a
// person (Publish) with the draft's ETag (its content digest) in If-Match, so nobody publishes
// content they have not seen; a must-fix finding that is not waived refuses it. Publishing is
// one transaction: the audit entry, a new version, the live pointer and the settings from the
// draft's metadata, the draft cleared.
//
// # Versions are never changed
//
// resource_versions is insert-only, and the database refuses UPDATE and DELETE on it (migration
// 0013). A rollback (Rollback) publishes an earlier version's content again as a new version
// that names it in RollbackOf. Bundles are stored once per address in blobs: the deterministic
// tar of bundle.Pack, addressed by the SHA-256 of its bytes.
//
// # Imports are idempotent and never delete
//
// Importing the same commit again changes nothing: content equal to the draft's is skipped,
// and content equal to the live version's leaves no draft. A resource the reader refuses keeps
// whatever the catalog had for it, and one that left the repository is not touched: a failed
// or partial read is unknown, not a deletion. Each resource is saved in its own transaction.
//
// # Platform overrides
//
// The repository's metadata suggests who gets a resource and how. A field changed on the
// platform (Configure) becomes an override: it takes effect at once, without a new version,
// and later changes of that field in the repository are reported (RepositoryDiffers), not
// applied, until the override is dropped.
//
// # Waivers
//
// A platform administrator may mark a must-fix finding of the suspected-secret rule as a false
// positive (Waive). The waiver is tied to the rule, the file and the finding's identifier,
// which follows the content of the line: when the line changes, the waiver no longer applies.
//
// # Writes
//
// Every write checks access (access.Service.CanIn, in the write's transaction), writes its
// audit entry in the same transaction and publishes its event (events.ResourceDraftUpdated,
// ResourcePublished, ResourceUpdated, ResourceDisabled, ResourceEnabled) once it has committed.
// A refused write is recorded in a transaction of its own. Audit entries are about the object
// kind "resource" with the resource's code as its id.
package catalog
