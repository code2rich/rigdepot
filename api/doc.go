// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package api holds the HTTP contract between the server, the command-line program and the web
// console: openapi.yaml (OpenAPI 3.0.3), and the oapi-codegen configurations that turn it into
// code.
//
// The contract comes first. To add or change an operation, edit openapi.yaml and run
//
//	go run ./tools/dev gen
//
// which regenerates the server interface in internal/server/httpapi and the client in
// internal/apiclient; the compiler then points at what has to follow. go run ./tools/dev check
// fails when the generated files no longer match the contract, and the tests in this package fail
// when the contract breaks its own conventions: every operation has examples, every example
// matches its schema, and the rules written at the top of openapi.yaml hold.
package api
