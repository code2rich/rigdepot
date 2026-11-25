// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package profileyaml parses a tool profile written in YAML into an adapter.Profile. Version: v0.
//
// A built-in adapter embeds its profile and reads it once:
//
//	//go:embed profile.yaml
//	var profileYAML []byte
//
//	func New() adapter.Adapter {
//		return adapter.FromProfile(profileyaml.MustParse(profileYAML))
//	}
//
// Parse checks the profile against the rules of schema/adapter/v1/profile.schema.json, so that a
// mistake in a profile shows up as a message that names the field instead of as an adapter that
// quietly writes nothing. The rules are written out in Go: this package is the one exception to
// "ext/ uses the standard library only" (it needs a YAML reader), and it stays small by not
// bringing a JSON Schema validator along. Its tests run it against the schema's own samples, so
// the two cannot drift apart unnoticed.
package profileyaml
