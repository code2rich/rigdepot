// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build !windows

package keyring

// protect returns what goes into a fallback file. On macOS and Linux there is no per-user
// encryption to use without a keychain; the file's permissions are the protection.
func protect(value []byte) ([]byte, error) { return value, nil }

func unprotect(data []byte) ([]byte, error) { return data, nil }
