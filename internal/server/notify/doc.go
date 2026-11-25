// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package notify delivers in-app notifications and dispatches to channels through an outbox.
//
// Nothing else in the server imports this package. Domain services publish events (package
// events); notify subscribes to them (Service.Subscribe), works out who should hear of what
// (Rules), writes the in-app notifications and queues mail in the outbox (Service.Enqueue).
//
// # Rules
//
// A Rule turns one event into Notices: to whom (a user id, or an e-mail address for someone
// without an account), which template with which parameters, a link into the console, and
// whether in the app, by mail or both. Rules find people through access (Directory, asked as the
// system): administrators, a product's owners, the owners who focus on a stage. Whoever caused
// an event is not told of it; disabled accounts hear nothing; mail goes only to people with an
// address. Rules lists the built-in rules and who each one reaches.
//
// The in-app notifications of an event are written in one transaction and its mail queued in
// another, so that mail that cannot be queued, and mail the server refuses later, never costs
// anyone their in-app notification.
//
// Events that notify must read without importing their publisher (catalog, feedback, the audit
// check) are defined in package events; access's events are its own.
//
// # In-app notifications
//
// One row per recipient in the notifications table holds the template and its parameters, not
// text: List renders them when they are read, in the reader's language. List pages newest first
// with an opaque cursor and counts the unread ones over all pages; MarkRead and MarkAllRead touch
// only the reader's own.
//
// # Invitations
//
// An invitation's link is a secret and is not in its event, so the HTTP layer hands it to
// MailInvite when the invitation is made. While mail is not set up MailInvite returns ErrMailOff
// and the administrator gets the link instead. The outbox forgets the link once the mail is sent
// (Template.SecretLink).
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
