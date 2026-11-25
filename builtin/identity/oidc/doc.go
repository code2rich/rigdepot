// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package oidc is the standard OpenID Connect identity source: the authorization code flow with
// PKCE (S256), a nonce, and the ID token checked by go-oidc (signature against the provider's
// key set, issuer, audience, expiry). It works with any provider that publishes a discovery
// document at <issuer>/.well-known/openid-configuration: Keycloak, Authentik, Okta, Entra ID,
// Google, Dex.
//
// Settings: the issuer and the client ID; the client secret is kept by the core. The source asks
// for the scopes openid, profile and email. The callback address to register at the provider is
// <server>/api/v1/auth/providers/<source id>/callback.
package oidc
