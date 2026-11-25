// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The little of Node the tests use, declared here instead of adding @types/node to the dependencies.
declare const process: {
  readonly env: Readonly<Record<string, string | undefined>>
  readonly platform: string
}

/** Node's Buffer, which Playwright wants for a file given as bytes. */
declare const Buffer: { from(data: Uint8Array): Uint8Array }
