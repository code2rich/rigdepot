// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import (
	"fmt"
	"slices"
	"time"

	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/resource"
)

// Origin is where a resource's content comes from.
type Origin string

// The origins of the resources table.
const (
	OriginRepo   Origin = "repo"   // a resource repository (a source)
	OriginUpload Origin = "upload" // an archive uploaded on the web or with rgd
	OriginWeb    Origin = "web"    // a form on the web (one-document kinds such as an MCP server)
)

// Status is where a resource is in its life.
type Status string

// The statuses of the resources table.
const (
	StatusDraft     Status = "draft"     // never published
	StatusPublished Status = "published" // live; it may have a newer draft
	StatusDisabled  Status = "disabled"  // taken off every computer
)

// Delivery is how an MCP server reaches the tools.
type Delivery string

// The deliveries of an MCP server's definition.
const (
	DeliveryNative   Delivery = "native"
	DeliveryOnDemand Delivery = "on_demand"
)

// Meta is the metadata stored with a draft, a version and a resource: the repository format's
// metadata as in effect (defaults filled in, secrets the files refer to added), plus what the
// catalog keeps beside it.
type Meta struct {
	resource.Meta
	// Delivery is the MCP server's delivery ("native", "on_demand"); empty for other kinds.
	Delivery Delivery `json:"delivery,omitempty"`
	// Description is what an AI tool reads to choose the resource (a skill's front matter
	// description), kept so that later checks can compare against it without its files.
	Description string `json:"description,omitempty"`
	// References are the codes of the resources this one uses (a workflow's steps, an agent's
	// bindings).
	References []string `json:"references,omitempty"`
}

// The fields a platform override can set (contract: OverridableField).
const (
	FieldName        = "name"
	FieldSummary     = "summary"
	FieldStages      = "stages"
	FieldAudience    = "audience"
	FieldScope       = "scope"
	FieldPolicy      = "policy"
	FieldWhen        = "when"
	FieldTry         = "try"
	FieldMaintainers = "maintainers"
	FieldDelivery    = "delivery"
)

// overridable lists the fields in the contract's order.
var overridable = []string{FieldName, FieldSummary, FieldStages, FieldAudience, FieldScope, FieldPolicy, FieldWhen, FieldTry, FieldMaintainers, FieldDelivery}

// Resource is one resource as its managers see it.
type Resource struct {
	ID         string
	Code       string
	Kind       string
	Origin     Origin
	SourceID   string // the source it is imported from; "" unless Origin is OriginRepo
	SourcePath string // its place in the repository
	// Scope is "org", "me" or a product code; ProductID is the product's id ("" when the
	// product is not on the server), OwnerID the author of a personal resource.
	Scope     resource.Scope
	ProductID string
	OwnerID   string
	Policy    resource.Policy
	Audience  resource.Audience
	Status    Status
	Version   int // the live version; 0 before the first publish
	Name      string
	Summary   string
	// Meta is the metadata in force: content's with the overrides applied.
	Meta        Meta
	Stages      []string
	Maintainers []string // user ids
	// Overrides are the fields set on the platform; RepositoryDiffers those of them whose
	// value in the content has changed since.
	Overrides         []string
	RepositoryDiffers []string
	DisabledReason    string
	References        []string // codes, of the live version
	Revision          int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
	PublishedAt       time.Time // of the live version; zero before the first publish

	// HasDraft: a draft is waiting. DraftCommit is the commit it was imported from.
	HasDraft    bool
	DraftCommit string
	// LiveCommit is the commit the live version was imported from.
	LiveCommit string
	// Checks counts the findings of the draft when there is one, otherwise of the live version.
	Checks Counts
	// Blocked: the draft or the live version has a must-fix finding that is not waived.
	Blocked bool
}

// ETag is the resource's entity tag: it changes with every change of the resource.
func (r Resource) ETag() string { return fmt.Sprintf(`"r-%s-%d"`, r.Code, r.Revision) }

// Counts are the findings that block and those that do not.
type Counts struct {
	Blocking int
	Advisory int
}

// Finding is one problem found in a resource, as stored and returned.
type Finding struct {
	// ID is stable for the same rule, file and line content (check.FindingIDs); waivers refer
	// to it.
	ID       string            `json:"id"`
	Rule     string            `json:"rule"`
	Severity extcheck.Severity `json:"severity"`
	// Message is the message key; Params fill it in.
	Message string         `json:"message"`
	Params  map[string]any `json:"params,omitempty"`
	File    string         `json:"file,omitempty"`
	Line    int            `json:"line,omitempty"`
	// FirstSeen is when this finding first appeared in the resource (Unix milliseconds),
	// carried over from earlier drafts and versions with the same ID.
	FirstSeen int64 `json:"first_seen"`
	// Waiver is set when the finding is marked as a false positive. Not stored with the
	// finding: waivers are applied when findings are read.
	Waiver *Waiver `json:"-"`
}

// Blocks reports whether the finding stops publishing.
func (f Finding) Blocks() bool { return f.Severity == extcheck.MustFix && f.Waiver == nil }

// Report is the result of checking one state of a resource.
type Report struct {
	Ruleset  int
	Findings []Finding
}

// Counts returns how many findings block and how many are advisory. A waived must-fix finding
// is neither.
func (r Report) Counts() Counts {
	var c Counts
	for _, f := range r.Findings {
		switch {
		case f.Blocks():
			c.Blocking++
		case f.Severity == extcheck.Advisory:
			c.Advisory++
		}
	}
	return c
}

// Waiver marks a finding as a false positive.
type Waiver struct {
	ID        string
	Rule      string
	File      string
	FindingID string
	Reason    string
	By        string // user id
	At        time.Time
}

// File is one file of a resource's content.
type File struct {
	Path string
	Size int
}

// Draft is the content waiting to be published.
type Draft struct {
	Code        string
	Kind        string
	Digest      resource.Digest
	Bundle      string // the bundle's address
	Meta        Meta
	Files       []File
	Checks      Report
	BaseVersion int // the live version it would replace; 0 when nothing is published
	Commit      string
	UpdatedBy   string // user id; "" for an import
	UpdatedAt   time.Time
}

// ETag is the draft's entity tag: its content digest. Publishing needs it in If-Match, so
// nobody publishes content they have not seen.
func (d Draft) ETag() string { return `"` + string(d.Digest) + `"` }

// Version is one published version. It never changes.
type Version struct {
	Number      int
	Digest      resource.Digest
	Bundle      string
	Meta        Meta
	Checks      Report
	Commit      string
	RollbackOf  int // for a rollback, the version whose content it is
	Note        string
	PublishedBy string // user id
	PublishedAt time.Time
}

// Ref is a short reference to another resource.
type Ref struct {
	Code, Kind, Name string
}

// Impact is who loses a resource when it is disabled or its scope narrowed.
type Impact struct {
	People       int
	Devices      int
	ReferencedBy []Ref
}

// sortedFields returns fs in the contract's order, each once.
func sortedFields(fs []string) []string {
	out := []string{}
	for _, f := range overridable {
		if slices.Contains(fs, f) {
			out = append(out, f)
		}
	}
	return out
}
