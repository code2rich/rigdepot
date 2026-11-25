// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// What the contract accepts for a new local account (AcceptInviteRequest, SetupAdminRequest in
// api/openapi.yaml), checked before sending so the person sees it at the field.

/** An account name: lower-case letters, digits, dot, underscore and hyphen; 2 to 32 characters. */
export const USERNAME = /^[a-z0-9][a-z0-9._-]{1,31}$/

/** The shortest password the server takes. */
export const MIN_PASSWORD = 12
