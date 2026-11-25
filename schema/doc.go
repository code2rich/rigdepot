// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package schema embeds the JSON Schemas of the formats other people write and read: the resource
// repository, the local manifest and tool adapter profiles.
//
// The schemas use JSON Schema draft 2020-12 and each has a stable $id. They are published as
// static files under that address, so that an editor can complete and validate a file whose
// first line names its schema:
//
//	# yaml-language-server: $schema=https://…/schema/repo/v1/rigdepot.schema.json
//
// A schema describes shape only: types, required fields, enumerations and patterns. What needs a
// second file to decide (a referenced resource exists, a secret is defined) is checked by the
// code that reads the format. Schemas do not forbid unknown fields, so that an older version of
// the programs can still read what a newer one wrote.
//
// # Layout
//
//	repo/v1/rigdepot.schema.json         rigdepot.yaml, the configuration of a resource repository
//	repo/v1/meta.schema.json             the metadata every resource carries
//	manifest/v1/manifest.schema.json     the local manifest
//	adapter/v1/profile.schema.json       a tool adapter profile
//
// The directory with the version in its name changes only in compatible ways. An incompatible
// change gets a new directory, and the old one stays readable for at least one major release.
//
// # Fixtures
//
// Every schema has samples next to it, in testdata/<schema>/valid and testdata/<schema>/invalid.
// The valid ones must pass. An invalid one is named "<field>--<what is wrong>", with the field
// written as a path with dots ("stages.1.id--upper-case.yaml"), and must fail at exactly that
// field. To change a schema, add the sample that shows why first.
package schema
