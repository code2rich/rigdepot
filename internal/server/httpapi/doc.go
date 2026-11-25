// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package httpapi is the HTTP layer: the implementation of the OpenAPI-generated interface,
// middleware and error mapping.
//
// api.gen.go is generated from api/openapi.yaml by "go run ./tools/dev gen" and is never edited
// by hand: it holds the request and response types, StrictServerInterface (one method per
// operation) and the routing on the standard library's ServeMux. NewHandler puts the pieces
// together and turns everything that fails before an operation is reached into a problem
// document. This layer has no business rules: it turns requests into calls of the domain
// packages and their results into responses.
package httpapi
