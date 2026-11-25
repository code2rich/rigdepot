// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package events

// The events of package feedback. They live here rather than in feedback because notify, which
// turns them into notifications, may not import feedback (docs/architecture/02-code-layout.md,
// rule R6). They carry ids and the feedback's number, never its title, text or context.

// FeedbackRef is the part every feedback event carries: which feedback and who is involved
// (user ids), so that a subscriber can find the recipients without reading the feedback.
type FeedbackRef struct {
	FeedbackID string `json:"feedback_id"`
	Number     int    `json:"number"` // as in /feedback/<number>
	// SubmitterID is empty for feedback the system raised (a friction lead).
	SubmitterID string `json:"submitter_id,omitempty"`
	// AssigneeID is who handles it; empty while nobody is assigned.
	AssigneeID   string `json:"assignee_id,omitempty"`
	ResourceCode string `json:"resource_code,omitempty"`
	ProductID    string `json:"product_id,omitempty"` // the product of the resource, if any
}

// The statuses of feedback, as in the contract's FeedbackStatus.
const (
	FeedbackNew        = "new"
	FeedbackAccepted   = "accepted"
	FeedbackInProgress = "in_progress"
	FeedbackResolved   = "resolved"
	FeedbackWontFix    = "wont_fix"
)

// FeedbackStatusChanged: feedback moved from Old to New. ActorID is who moved it: the handler,
// or the submitter whose low rating reopened it; empty for the system.
type FeedbackStatusChanged struct {
	FeedbackRef
	Old     string `json:"old"`
	New     string `json:"new"`
	ActorID string `json:"actor_id,omitempty"`
}

// EventName implements Payload.
func (FeedbackStatusChanged) EventName() string { return "feedback.status_changed" }

// FeedbackCommented: someone replied on feedback. The reply itself is not in the event.
type FeedbackCommented struct {
	FeedbackRef
	ActorID string `json:"actor_id"`
}

// EventName implements Payload.
func (FeedbackCommented) EventName() string { return "feedback.commented" }
