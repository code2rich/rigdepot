// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Where to go after signing in. The sign-in page gets it as ?next=<console path> (app.tsx signInPath);
// only a path inside the console is followed, so a link cannot send someone off to another site.

/** safeNext returns the console path to go to after signing in: next when it is one, else home. */
export function safeNext(next: string | undefined): string {
  if (!next || !next.startsWith('/') || next.startsWith('//') || next.includes('\\')) return '/'
  // Control characters and the sign-in page itself (which would loop) are not followed either.
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f]/.test(next) || next === '/login' || next.startsWith('/login?')) return '/'
  return next
}
