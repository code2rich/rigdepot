// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package events

// The events of package catalog. They live here rather than in catalog because notify, which
// turns them into notifications, may not import catalog (docs/architecture/02-code-layout.md,
// rule R6). Scope is "org", "me" or a product code; ProductID is set for a product scope.

// ResourceRef is the part every catalog event carries: which resource, its name at the time, who
// it is for and who maintains it (user ids), so that a subscriber can find the recipients
// without reading the catalog.
type ResourceRef struct {
	ResourceID  string   `json:"resource_id"`
	Code        string   `json:"code"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Scope       string   `json:"scope"`
	ProductID   string   `json:"product_id,omitempty"`
	Stages      []string `json:"stages,omitempty"`
	Maintainers []string `json:"maintainers,omitempty"`
}

// ResourceDraftUpdated: a resource has a new draft waiting to be published (imported from its
// repository, uploaded, or registered on the web). ActorID is empty for an import.
type ResourceDraftUpdated struct {
	ResourceRef
	Digest  string `json:"digest"`
	Created bool   `json:"created"` // the draft created the resource
	ActorID string `json:"actor_id,omitempty"`
}

// EventName implements Payload.
func (ResourceDraftUpdated) EventName() string { return "catalog.draft_updated" }

// ResourcePublished: a new version is live. RollbackOf is set when its content is an earlier
// version's.
type ResourcePublished struct {
	ResourceRef
	Version    int    `json:"version"`
	Previous   int    `json:"previous,omitempty"` // 0 for the first version
	RollbackOf int    `json:"rollback_of,omitempty"`
	ActorID    string `json:"actor_id"`
}

// EventName implements Payload.
func (ResourcePublished) EventName() string { return "catalog.published" }

// ResourceUpdated: who gets a resource or how changed on the platform (scope, policy, stages,
// maintainers…) without a new version.
type ResourceUpdated struct {
	ResourceRef
	Fields  []string `json:"fields"`
	ActorID string   `json:"actor_id"`
}

// EventName implements Payload.
func (ResourceUpdated) EventName() string { return "catalog.updated" }

// ResourceDisabled: a resource is taken off every computer at their next sync.
type ResourceDisabled struct {
	ResourceRef
	ActorID string `json:"actor_id"`
}

// EventName implements Payload.
func (ResourceDisabled) EventName() string { return "catalog.disabled" }

// ResourceEnabled: a disabled resource is back.
type ResourceEnabled struct {
	ResourceRef
	ActorID string `json:"actor_id"`
}

// EventName implements Payload.
func (ResourceEnabled) EventName() string { return "catalog.enabled" }
