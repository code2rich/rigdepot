// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package feedback runs the feedback queue, its state machine and ratings
// (docs/subsystems/feedback-friction.md).
//
// # Who sees and who handles
//
// Everyone sees what they submitted and what they are assigned. Feedback about a resource is
// handled by whoever access.ActionFeedbackManage allows on that resource (the product's owners
// for a product resource, the resource's maintainers, platform administrators); feedback about
// no resource by platform administrators. Handlers see what they handle. Anyone who sees
// feedback may reply; only handlers change its status, assignee or severity, and only to an
// assignee who can handle it too.
//
// # States
//
// new → accepted → in_progress → resolved, or wont_fix, which needs a reason
// (ErrReasonRequired). A handler may move feedback to any of these but back to new, also out of
// a closed state. A status change on feedback nobody is assigned to assigns its handler. Once
// resolved, the submitter may score it 1 to 5 (Rate); a score of LowScore or less reopens it
// (accepted) and tells the handler. A new resolution clears the score, so the score always
// belongs to the last resolution.
//
// # Context
//
// The context keeps only the agreed fields for the way the feedback came in (cleanContext):
// page and browser from the console; command-line and tool versions, the operating system, the
// last sync's result and the session's friction counts from the command line, also when an AI
// tool drafted it. Other fields are dropped. There is no field for conversation content, code,
// logs or file paths, so none can be stored.
//
// # Writes
//
// Submitting and handling check access and write their audit entry (feedback.create,
// feedback.manage) in the same transaction; a refused one is recorded on its own. The audit
// entry names the feedback by number and never holds its title, text or context. Replies and
// scores are on the feedback's own timeline (feedback_events) and not in the audit log. Status
// changes and replies are published as events.FeedbackStatusChanged and
// events.FeedbackCommented once committed; notify turns them into notifications.
package feedback
