// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package identity is the identity source extension point (E3). Version: v0.
//
// An identity source signs people in through a system the organization already has: an OIDC
// provider, GitLab, later SAML. It does one thing: turn a browser's round trip through that
// system into an Identity. Everything around it is the core's: which sources are configured and
// switched on, the encrypted cookie that carries the state between Begin and Complete, linking
// the identity to an account (by the source and its subject, then by a verified e-mail address,
// else a new account that still has to ask to join), refusing disabled accounts, sessions and the
// audit log.
//
// # The round trip
//
//  1. Begin returns the address to send the browser to and an opaque state. The core keeps the
//     state in a short-lived, encrypted, tamper-proof cookie; the provider does not store
//     anything.
//  2. The browser comes back to the callback address with query parameters. Complete gets the
//     same configuration, the state from Begin and those parameters, and returns the identity.
//     It checks everything a protocol asks for (OIDC: the state parameter, PKCE, the ID token's
//     signature, issuer, audience, expiry and nonce). Any failure is an error, and an error
//     refuses the sign-in: there is no "partial" identity.
//
// # Configuration
//
// A source is configured on the web. ConfigSchema is a JSON Schema of the settings the settings
// form shows (an object: an issuer, a client ID). The client secret is not part of it: the core
// asks for it separately, seals it with the master key at once, never returns it in any
// response, and hands it to the provider in Config.ClientSecret. A provider must not put it into
// an error, a log line or the address Begin returns; the core still masks it in every error
// before showing or recording one.
//
// # Network
//
// A provider talks to the addresses in its configuration and nowhere else. A source that is
// compiled in but not configured sends nothing.
//
// # Adding a source
//
// A built-in source is one directory under builtin/identity with New exported, a test against a
// local fake server (identitytest has an OIDC one; tests use no network), and one line in
// builtin/all/server:
//
//	identity.Register(saml.New())
//
// # Stability
//
// Everything under ext/ is public API. Before 1.0 it may change in breaking ways, and every such
// change is listed in the release notes. From 1.0 on, a breaking change means a new version of
// the package, and the old one stays for at least one major release.
package identity
