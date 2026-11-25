// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The addresses typed into the invitation dialog: one per line (commas, semicolons and spaces work too).

const ADDRESS = /^[^\s@,;]+@[^\s@,;]+\.[^\s@,;]+$/

/** parseEmails splits what was typed into addresses that look right and ones that do not, without repeats. */
export function parseEmails(text: string): { valid: string[]; invalid: string[] } {
  const all = [
    ...new Set(
      text
        .split(/[\s,;]+/)
        .map((s) => s.trim())
        .filter(Boolean),
    ),
  ]
  return { valid: all.filter((a) => ADDRESS.test(a)), invalid: all.filter((a) => !ADDRESS.test(a)) }
}
