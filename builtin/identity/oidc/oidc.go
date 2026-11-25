// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package oidc

import (
	"context"
	"net/url"
	"strings"

	"github.com/code2rich/rigdepot/builtin/identity/internal/oidcflow"
	"github.com/code2rich/rigdepot/ext/identity"
)

// Provider is the OIDC identity source.
type Provider struct {
	flows *oidcflow.Client
}

// New returns the OIDC identity source.
func New() *Provider { return &Provider{flows: oidcflow.New()} }

// ID implements identity.Provider.
func (*Provider) ID() string { return "oidc" }

// Name implements identity.Provider.
func (*Provider) Name() string { return "OpenID Connect" }

const schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["issuer", "client_id"],
  "additionalProperties": false,
  "properties": {
    "issuer": {
      "type": "string", "format": "uri", "maxLength": 2048,
      "title": "Issuer",
      "description": "The provider's issuer, exactly as its discovery document says, such as https://sso.example.com/realms/dev."
    },
    "client_id": {"type": "string", "minLength": 1, "maxLength": 256, "title": "Client ID"},
    "scopes": {
      "type": "array", "maxItems": 16, "items": {"type": "string", "pattern": "^[!#-\\[\\]-~]+$"},
      "title": "Extra scopes",
      "description": "Requested besides openid, profile and email."
    }
  }
}`

// ConfigSchema implements identity.Provider.
func (*Provider) ConfigSchema() []byte { return []byte(schema) }

func flow(cfg identity.Config) oidcflow.Flow {
	scopes := []string{"openid", "profile", "email"}
	if extra, ok := cfg.Settings["scopes"].([]any); ok {
		for _, s := range extra {
			if s, ok := s.(string); ok && s != "" {
				scopes = append(scopes, s)
			}
		}
	}
	return oidcflow.Flow{
		Issuer: strings.TrimSpace(cfg.String("issuer")), ClientID: strings.TrimSpace(cfg.String("client_id")), ClientSecret: cfg.ClientSecret,
		Scopes: scopes, UsernameClaims: []string{"preferred_username", "nickname"}, GroupsClaim: "groups",
	}
}

// Validate implements identity.Provider.
func (*Provider) Validate(cfg identity.Config) error {
	f := flow(cfg)
	if err := oidcflow.CheckURL("issuer", f.Issuer); err != nil {
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
