// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package notify delivers in-app notifications and dispatches to channels through an outbox.
//
// Nothing else in the server imports this package. Domain services publish events (package
// events); notify subscribes to them (Service.On), works out who should hear of what, and queues
// mail in the outbox (Service.Enqueue). The in-app notifications and the rules from events to
// recipients are task M1-2F; so far this package holds the outbox and the channels.
//
// # The outbox
//
// Queuing a mail is one row in mail_outbox, written in the transaction of the caller. Sending is
// a background job ("mail-outbox", every 30 seconds and right after something was queued), so a
// slow, failing or missing mail server never slows down or fails the change that caused the
// mail. A failed attempt is retried after 1, 2, 4 … minutes, at most an hour apart, until 24
// hours have passed since the first attempt; then the mail is marked failed, the readiness check
// reports it, and RetryFailed queues it again. The error stored with it has the channel's secret
// settings masked.
//
// While no mail server is configured, nothing is sent and nothing is marked failed: mail waits
// in the outbox and goes out once one is.
//
// At most HourlyLimit messages (30 by default) go to one recipient in an hour. When only one
// more is allowed and more are waiting, the rest are merged into one digest.
//
// # Language
//
// A mail is queued with a template name, parameters and the recipient's language, and rendered
// when it is sent, in that language (the recipient's, never the language of whoever caused it).
// A template is a subject and a body in the "server" message domain (locales/*/server.json).
package notify
