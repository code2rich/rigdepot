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
// # Kinds and payloads
//
// Every kind of event is one Go type in this package that implements Payload, so that the
// packages that subscribe (notify, the module host) can read it without importing the package
// that published it. Payloads hold identifiers and short labels, never content or secrets, and
// must encode to JSON: optional modules receive them as JSON.
package events
