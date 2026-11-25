// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package apiclient is the client for the server API; its types are generated from the OpenAPI
// contract.
//
// api.gen.go is generated from api/openapi.yaml by "go run ./tools/dev gen" and is never edited
// by hand. ClientWithResponses has one method per operation, returning the decoded body of each
// documented status (JSON200, ApplicationproblemJSONDefault, …).
package apiclient
