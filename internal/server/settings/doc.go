// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package settings stores runtime settings and the state of the setup wizard in the database.
//
// Run-time settings are what an administrator changes on the web (the organization's name, the
// mail server, which optional modules are on) as opposed to the start-up configuration in the
// environment. Each one is a row of the settings table. Sensitive ones (the mail password) are
// sealed with the master key, bound to their key, and never leave the package except to the
// code that uses them (Service.Mail); the API only says whether they are set. Every change
// goes into the audit log in the same transaction, secrets as "replaced".
//
// The table also holds what the server keeps for itself: the audit log's chain key (AuditKey),
// sealed, and the setup wizard's state and one-time setup token (decision D7 of
// docs/architecture/13-open-questions.md): generated and printed in the log on first start,
// unusable once the wizard is complete.
//
// Permissions: settings may not import access (rule R6), so the server hands it an Authorizer
// that asks access. Without one every protected operation is refused.
package settings
