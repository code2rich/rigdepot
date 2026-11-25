// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package notify is the notification channel extension point (E10). Version: v0.
//
// A channel does one thing: deliver a message that is already rendered, to one recipient, with
// the configuration the deployment saved for it. Who gets a message, in which language, when it
// is sent and whether a failure is retried are all the core's business: the core keeps an outbox,
// sends from a background job, retries with backoff, and keeps the in-app notification whatever
// happens to the channel.
//
// # Configuration
//
// ConfigSchema returns a JSON Schema of the channel's settings; the settings page renders its
// form from it. Mark every secret field (a password, a token) with "writeOnly": true. The core
// never shows those values again, and masks them, and their base64 forms, in every error a
// channel returns before the error is logged or stored. A channel must still not put them in
// its errors in the first place.
//
// A channel that is compiled in but not configured does nothing: the core does not call it, so
// it sends no request anywhere.
//
// # What a channel may send
//
// Only the fields of Message, and only to the addresses in its configuration or the recipient's
// address. It must not fetch anything else, nor take an address from the message's text.
//
// # Adding a channel
//
// A built-in channel is one directory under builtin/notifiers with New exported, a test against
// a local fake server (tests use no network), and one line in builtin/all/server:
//
//	notify.Register(webhook.New())
//
// # Stability
//
// Everything under ext/ is public API. Before 1.0 it may change in breaking ways, and every such
// change is listed in the release notes. From 1.0 on, a breaking change means a new version of
// the package, and the old one stays for at least one major release.
package notify
