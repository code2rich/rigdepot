// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package gitlab is the GitLab identity source: OIDC with GitLab's defaults filled in. GitLab
// (self-managed or gitlab.com) is an OpenID Connect provider itself, with its discovery document
// at <GitLab address>/.well-known/openid-configuration, so the settings are only the GitLab
// address and the application's ID; the secret is kept by the core. The source asks for the
// scopes openid, profile and email, takes the account name from GitLab's nickname claim and the
// groups from groups_direct. Joining products by GitLab group automatically, which would need
// GitLab's own API, is not done yet.
//
// In GitLab: Admin area (or a group) > Applications, with the scopes openid, profile and email,
// and the callback address <server>/api/v1/auth/providers/<source id>/callback.
package gitlab
