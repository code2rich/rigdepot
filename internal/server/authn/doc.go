// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package authn handles sign-in and sessions: local accounts, identity source orchestration,
// access tokens and the device code flow.
//
// # Credentials
//
// Three kinds of caller, one credential each (docs/architecture/06-server.md#鉴权):
//
//   - a browser holds a session: 256 random bits in a cookie, ending after
//     SessionIdle without use and SessionMax after it began;
//   - the command-line program holds a device token (DeviceTokenPrefix),
//     which it gets through the device code flow and Devices verifies
//     (device.go): bound to one computer and its X25519 public key, ended
//     by signing the computer out or 90 days without a request;
//   - a program holds an access token (AccessTokenPrefix) with a scope and
//     an expiry.
//
// Only digests of credentials are stored; a plain token exists in the one
// response that issues it. Session, AccessToken and Device turn a credential
// into a Caller, whose Actor the HTTP layer puts into the request's context
// (access.WithActor). They read the account's status on every call, so a
// disabled account is refused with its next request; permissions are not
// kept in any credential either: access decides them from the database for
// every action. What an access token may do is its scope intersected with
// what its owner may do at that moment (access.Actor.Scope).
//
// # Local accounts
//
// Passwords are hashed with argon2id (about 19 MiB per hash), and at most a
// few hashes run at once, so that a flood of sign-ins cannot exhaust a small
// machine. There is no default password: the first administrator chooses one
// in the setup wizard, everyone else when accepting an invitation. Wrong
// passwords lock the account progressively (lockFor); every attempt, failed
// or not, is in the audit log in the login category, without the password.
package authn
