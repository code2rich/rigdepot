// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package access manages users, products, members, roles, visibility decisions, access requests
// and invitations.
//
// # Permissions
//
// Every domain service asks Service.Can before it changes anything: may this
// Actor do this Action to this Target. The answer comes from one table of
// rules (action.go) applied to what the database says about the actor at that
// moment: their account status, whether they are a platform administrator,
// and their role in each product. Nothing is cached, so disabling an account
// or taking someone out of a product takes effect with their next request.
// An access token can do at most what its scope covers, intersected with what
// its owner may do.
//
// The three roles and three scopes are those of
// docs/subsystems/access-roles.md; matrix_test.go writes the whole table out
// by role and scope and is the test to change first when a rule changes.
//
// # Invariants
//
// The platform keeps at least one active administrator and a product that
// has owners keeps at least one. Both are checked in the transaction that
// would break them, under a lock (dbx.Tx.Lock) every such change takes, so
// two changes at the same moment cannot each leave the other as the last
// one. A product may be created without owners; once it has one it keeps one.
//
// # Writes
//
// Every write runs in one transaction with its audit entry (Auditor): when
// the entry cannot be written, the change does not happen. A denied write is
// recorded too, in a transaction of its own. Events (Publisher) go out after
// the commit.
//
// The Actor of a request is in its context (WithActor, ActorFrom): the
// authentication middleware puts it there, the HTTP layer takes it out.
package access
