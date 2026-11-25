// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package events carries in-process domain events for notifications and modules to subscribe to.
//
// A domain service that changes something others may care about publishes an event in the
// transaction that makes the change:
//
//	err := db.InTx(ctx, func(tx *dbx.Tx) error {
//		// … write the change and its audit entry …
//		bus.Publish(tx, events.ResourcePublished{…})
//		return nil
//	})
//
// The event is delivered only after the transaction has committed, so nobody hears of a change
// that was rolled back. Delivery is asynchronous: each subscriber has its own queue and
// goroutine, gets the events in the order they were published, and cannot slow down or fail the
// change that caused them. A subscriber that returns an error or panics is logged; the event is
// not delivered again.
//
// Events live in memory only. An event published just before the process dies may be lost, so a
// subscriber must not be the only record of anything: the change itself is in the database, and
// what must survive a restart (a mail to send) is written to the database by the subscriber.
//
// A domain package that collects its events and hands them over after its own commit (access
// does, through access.Publisher) gets the bus through the adapter Publisher.
//
// # Names and payloads
//
// Every kind of event is one Go type with an EventName method, such as
// access.MembershipChanged ("access.membership_changed"). The type lives with the package that
// publishes it when every subscriber may import that package (notify may import access, by rule
// R6); otherwise it lives here, so that subscribers can read it without importing the publisher.
// Payloads hold identifiers and short labels, never content or secrets, and must encode to
// JSON: optional modules receive them as JSON.
package events
