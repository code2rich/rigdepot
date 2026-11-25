// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package identity

import (
	"context"
	"errors"
	"fmt"
	"net/url"
)

// Provider is one kind of identity source: OIDC, GitLab, SAML. The core configures any number of
// sources of each kind; every method gets the configuration of the one it acts for.
type Provider interface {
	// ID is the kind: lower-case letters, digits and single hyphens, such as "oidc". It never
	// changes once released: saved sources are keyed by it.
	ID() string
	// Name is the kind's name as people know it: a protocol or a product ("OpenID Connect",
	// "GitLab"). It is not translated.
	Name() string
	// ConfigSchema is a JSON Schema (draft 2020-12) of an object: the settings of one source,
	// without the client secret. The settings form is rendered from it, and the core refuses
	// settings that do not match it. Set "additionalProperties": false, so that nothing secret
	// is saved where it would be shown again.
	ConfigSchema() []byte
	// Validate checks settings beyond what the schema says (an issuer must be https). It
	// returns a *ConfigError, or nil. It sends nothing anywhere.
	Validate(cfg Config) error
	// Begin starts a sign-in: the address to send the browser to, and the state Complete needs.
	// callbackURL is where the provider has to send the browser back to.
	Begin(ctx context.Context, cfg Config, callbackURL string) (redirectURL string, state []byte, err error)
	// Complete turns the browser's return into an identity. query is the callback's query.
	// Every failure is an error, and refuses the sign-in: ErrDeclined when the person said no,
	// ErrState when the state does not match the callback, anything else when the provider or
	// its answer cannot be trusted.
	Complete(ctx context.Context, cfg Config, state []byte, query url.Values) (Identity, error)
	// Test checks that a source works without anyone signing in: its address answers, and the
	// client ID and secret are accepted when the protocol can tell without a person. A failure
	// is a *TestError naming the step. callbackURL is the source's callback address.
	Test(ctx context.Context, cfg Config, callbackURL string) error
}

// Config is the configuration of one source.
type Config struct {
	// Settings are the values shaped by the kind's ConfigSchema. Numbers are json.Number or
	// float64.
	Settings map[string]any
	// ClientSecret is the secret the source shares with the identity provider; "" when none is
	// set. It is never shown again after it was entered.
	ClientSecret string
}

// String returns the setting under key as a string; "" when it is missing or not a string.
func (c Config) String(key string) string {
	s, _ := c.Settings[key].(string)
	return s
}

// Identity is a person as an identity source vouches for them.
type Identity struct {
	// Subject identifies the person at this source and never changes (OIDC's sub). Required.
	Subject string
	// Email is the person's address; "" when the source does not say.
	Email string
	// EmailVerified says that the source checked the person owns Email. Only a verified address
	// links the identity to an existing account that has the same address.
	EmailVerified bool
	// Username is the account name the person has at the source, a suggestion for a new
	// account's name; may be empty.
	Username string
	// DisplayName is the person's name; may be empty.
	DisplayName string
	// Groups are the groups the source says the person is in, when it says; for joining
	// products automatically later.
	Groups []string
}

var (
	// ErrDeclined means the person declined at the identity provider, or it refused them.
	ErrDeclined = errors.New("identity: the identity provider did not sign the person in")
	// ErrState means the callback does not belong to the sign-in that the state is from: it is
	// stale, forged or replayed.
	ErrState = errors.New("identity: the callback does not match the sign-in that was started")
)

// ConfigError says which setting is not usable.
type ConfigError struct {
	Field string // the setting's name, such as "issuer"
	Code  string // what is wrong: "required", "format", "https"…
}

func (e *ConfigError) Error() string { return fmt.Sprintf("identity: setting %s: %s", e.Field, e.Code) }

// The steps of Test.
const (
	StepConfig    = "config"    // the settings are not usable
	StepDiscovery = "discovery" // the provider's address does not answer as it should
	StepClient    = "client"    // the provider refused the client ID or secret
)

// TestError is a failed Test.
type TestError struct {
	Step string // one of the Step constants
	Err  error
}

func (e *TestError) Error() string { return "identity: " + e.Step + ": " + e.Err.Error() }
func (e *TestError) Unwrap() error { return e.Err }
