// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package testenv isolates tests from the machine they run on: a temporary home directory, a
// scrubbed environment, the system keychain switched off, loopback-only networking and failing
// stand-ins for external commands.
package testenv
