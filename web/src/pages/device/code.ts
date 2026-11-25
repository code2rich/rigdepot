// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The confirmation code `rgd login` shows in the terminal (KQ7M-2XHP): eight letters and digits, typed
// with or without the hyphen, in any case.

/** normalizeCode turns what someone typed into the code's canonical form, or "" when it is not one. */
export function normalizeCode(raw: string | undefined): string {
  const bare = (raw ?? '').toUpperCase().replace(/[\s-]/g, '')
  return /^[A-Z0-9]{8}$/.test(bare) ? `${bare.slice(0, 4)}-${bare.slice(4)}` : ''
}

/** osName is how people call an operating system the command line reports by its Go name. */
export function osName(os: string): string {
  return { darwin: 'macOS', linux: 'Linux', windows: 'Windows', freebsd: 'FreeBSD' }[os] ?? os
}
