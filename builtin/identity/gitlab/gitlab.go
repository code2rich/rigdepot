// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package gitlab

import (
	"context"
	"net/url"
	"strings"

	"github.com/code2rich/rigdepot/builtin/identity/internal/oidcflow"
	"github.com/code2rich/rigdepot/ext/identity"
)

// Provider is the GitLab identity source.
type Provider struct {
	flows *oidcflow.Client
}

// New returns the GitLab identity source.
func New() *Provider { return &Provider{flows: oidcflow.New()} }

// ID implements identity.Provider.
func (*Provider) ID() string { return "gitlab" }

// Name implements identity.Provider.
func (*Provider) Name() string { return "GitLab" }

const schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["url", "client_id"],
  "additionalProperties": false,
  "properties": {
    "url": {
      "type": "string", "format": "uri", "maxLength": 2048,
      "title": "GitLab address",
      "description": "Such as https://gitlab.example.com, or https://gitlab.com."
    },
    "client_id": {"type": "string", "minLength": 1, "maxLength": 256, "title": "Application ID"}
  }
}`

// ConfigSchema implements identity.Provider.
func (*Provider) ConfigSchema() []byte { return []byte(schema) }

// flow is GitLab's OIDC: the issuer is the GitLab address without a trailing slash, which is
// how GitLab writes it in its discovery document.
func flow(cfg identity.Config) oidcflow.Flow {
	return oidcflow.Flow{
		Issuer: strings.TrimRight(strings.TrimSpace(cfg.String("url")), "/"), ClientID: strings.TrimSpace(cfg.String("client_id")),
		ClientSecret: cfg.ClientSecret, Scopes: []string{"openid", "profile", "email"},
		UsernameClaims: []string{"nickname", "preferred_username"}, GroupsClaim: "groups_direct",
	}
}

// Validate implements identity.Provider.
func (*Provider) Validate(cfg identity.Config) error {
	f := flow(cfg)
	if err := oidcflow.CheckURL("url", f.Issuer); err != nil {
		return err
	}
	return oidcflow.CheckClientID("client_id", f.ClientID)
}

// Begin implements identity.Provider.
func (p *Provider) Begin(ctx context.Context, cfg identity.Config, callbackURL string) (string, []byte, error) {
	if err := p.Validate(cfg); err != nil {
		return "", nil, err
	}
	return p.flows.Begin(ctx, flow(cfg), callbackURL)
}

// Complete implements identity.Provider.
func (p *Provider) Complete(ctx context.Context, cfg identity.Config, state []byte, query url.Values) (identity.Identity, error) {
	if err := p.Validate(cfg); err != nil {
		return identity.Identity{}, err
	}
	return p.flows.Complete(ctx, flow(cfg), state, query)
}

// Test implements identity.Provider.
func (p *Provider) Test(ctx context.Context, cfg identity.Config, callbackURL string) error {
	if err := p.Validate(cfg); err != nil {
		return &identity.TestError{Step: identity.StepConfig, Err: err}
	}
	return p.flows.Test(ctx, flow(cfg), callbackURL)
}
