// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package feedback

import (
	"errors"
	"fmt"
	"time"
)

// Type is what sort of feedback it is.
type Type string

// The types of feedback (contract: FeedbackType).
const (
	TypeBug        Type = "bug"
	TypeSuggestion Type = "suggestion"
	TypeQuestion   Type = "question"
	TypeFriction   Type = "friction" // raised by the system from usage data (M2); never submitted
)

// Status is where feedback is in its handling.
type Status string

// The statuses (contract: FeedbackStatus). new → accepted → in_progress → resolved, or
// wont_fix with a reason; a low score reopens resolved feedback.
const (
	StatusNew        Status = "new"
	StatusAccepted   Status = "accepted"
	StatusInProgress Status = "in_progress"
	StatusResolved   Status = "resolved"
	StatusWontFix    Status = "wont_fix"
)

// Statuses are all statuses, in the order of the flow.
var Statuses = []Status{StatusNew, StatusAccepted, StatusInProgress, StatusResolved, StatusWontFix}

// Closed reports whether feedback in s needs nothing more from its handler.
func (s Status) Closed() bool { return s == StatusResolved || s == StatusWontFix }

// Origin is how feedback came in.
type Origin string

// The origins (contract: FeedbackOrigin).
const (
	OriginWeb    Origin = "web"
	OriginAI     Origin = "ai" // drafted by an AI tool and confirmed by the person
	OriginCLI    Origin = "cli"
	OriginSystem Origin = "system"
)

// Severity is how bad the handler (or the submitter) thinks it is.
type Severity string

// The severities (contract: FeedbackSeverity).
const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

// Context is the scene the feedback was sent from: the agreed fields and nothing else
// (docs/subsystems/feedback-friction.md#入口). There is no field for conversation content,
// code, file paths, logs or call arguments, so none can be stored.
type Context struct {
	Page        string          `json:"page,omitempty"`    // console page (web)
	Browser     string          `json:"browser,omitempty"` // (web)
	Tool        string          `json:"tool,omitempty"`    // the AI tool (command line, AI-drafted)
	ToolVersion string          `json:"tool_version,omitempty"`
	CLIVersion  string          `json:"cli_version,omitempty"`
	OS          string          `json:"os,omitempty"`
	LastSync    string          `json:"last_sync,omitempty"` // ok | partial | failed
	Frictions   []FrictionCount `json:"frictions,omitempty"` // this session's frictions, counts only
	Trigger     string          `json:"trigger,omitempty"`   // the rule that raised friction feedback (system)
}

// FrictionCount is how often one kind of friction happened.
type FrictionCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// ResourceRef is the resource feedback is about, as it is named now.
type ResourceRef struct {
	Code, Kind, Name string
}

// Feedback is one piece of feedback.
type Feedback struct {
	ID              string
	Number          int // shown as FB-<number>
	Type            Type
	Origin          Origin
	Status          Status
	StatusReason    string
	Severity        Severity
	Title           string
	Body            string
	SubmitterID     string // "" for system feedback
	AssigneeID      string
	ResourceID      string
	Resource        *ResourceRef // nil without a resource, or when it no longer exists
	ResourceVersion int
	Context         Context
	Score           int // 1-5 for the last resolution; 0 while not rated
	ScoreNote       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// Events is the timeline, oldest first; only Get fills it.
	Events []Event
}

// EventKind is what happened on the timeline.
type EventKind string

// The kinds of timeline events (contract: FeedbackEvent.kind).
const (
	EventCreated  EventKind = "created"
	EventComment  EventKind = "comment"
	EventStatus   EventKind = "status"
	EventAssigned EventKind = "assigned"
	EventRated    EventKind = "rated"
	EventReopened EventKind = "reopened"
)

// Event is one entry of the timeline.
type Event struct {
	ID         string
	Kind       EventKind
	ActorID    string // "" for the system
	Body       string // the comment, or the reason given with a status change or a score
	Status     Status // for status and reopened
	AssigneeID string // for assigned; "" when unassigned
	Score      int    // for rated
	At         time.Time
}

// Submission is feedback someone submits.
type Submission struct {
	Type            Type
	Title           string
	Body            string
	Severity        Severity // "" is medium
	Resource        string   // a resource's code; "" for none
	ResourceVersion int      // 0 for none
	AIDrafted       bool     // an AI tool drafted it and the person confirmed it
	Context         Context
}

// Change is what a handler changes; nil fields stay.
type Change struct {
	Status       *Status
	StatusReason *string
	Assignee     *string // a user id
	Unassign     bool
	Severity     *Severity
}

// Filter narrows a list.
type Filter struct {
	Status       Status
	Type         Type
	Resource     string // a resource's code
	Mine         bool   // only what the caller submitted
	AssignedToMe bool
}

// Summary counts feedback by status.
type Summary struct {
	Open     int // neither resolved nor declined
	ByStatus map[Status]int
}

// Kind is what sort of failure an Error is; the HTTP layer maps it to a status.
type Kind int

// The kinds of failure.
const (
	KindInvalid  Kind = iota + 1 // the input is not valid; Fields says which
	KindDenied                   // the actor may not do this
	KindNotFound                 // it does not exist, or the actor may not see it
	KindConflict                 // the state does not allow it
)

// Error is a failure the caller can act on. Code is the problem document's code.
type Error struct {
	Kind   Kind
	Code   string
	Fields []FieldError
}

// FieldError is one invalid field of the input.
type FieldError struct {
	Field, Code, Detail string
}

func (e *Error) Error() string {
	if len(e.Fields) > 0 {
		return fmt.Sprintf("feedback: %s %v", e.Code, e.Fields)
	}
	return "feedback: " + e.Code
}

// Is matches errors of the same code.
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == e.Code
}

// The failures of this package.
var (
	ErrDenied         = &Error{Kind: KindDenied, Code: "access.denied"}
	ErrNotFound       = &Error{Kind: KindNotFound, Code: "object.not_found"}
	ErrInvalid        = &Error{Kind: KindInvalid, Code: "request.invalid"}
	ErrReasonRequired = &Error{Kind: KindInvalid, Code: "feedback.reason_required"}
	ErrNotResolved    = &Error{Kind: KindConflict, Code: "feedback.not_resolved"}
)

func invalid(fields ...FieldError) error {
	return &Error{Kind: KindInvalid, Code: ErrInvalid.Code, Fields: fields}
}
