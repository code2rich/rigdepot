// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package apiclient is the client for the server API; its types are generated from the OpenAPI
// contract.
//
// api.gen.go is generated from api/openapi.yaml by "go run ./tools/dev gen" and is never edited
// by hand. ClientWithResponses has one method per operation, returning the decoded body of each
// documented status (JSON200, ApplicationproblemJSONDefault, …).
//
// connect.go is the thin layer the commands use: Connect builds the client for a server with a
// timeout, the program's User-Agent, a bearer token and the trusted certificate authorities;
// Check turns an answer that is not a success into an *Error with the problem document;
// Unreachable tells a network failure from the server's answer.
package apiclient
