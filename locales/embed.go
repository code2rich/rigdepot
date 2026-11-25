// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package locales embeds the message catalogues: one directory per language
// (a BCP 47 tag such as en or zh-CN), one JSON file per part of the product.
// See README.md for how to add a language; it needs no code.
package locales

import "embed"

// FS holds <tag>/<domain>.json and <tag>/meta.json for every language.
//
//go:embed */*.json
var FS embed.FS
