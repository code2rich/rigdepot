// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import { describe, expect, it } from 'vitest'
import { parseEmails } from './emails'

describe('parseEmails', () => {
  it('splits lines and separators, drops repeats, and keeps the ones that are not addresses apart', () => {
    expect(parseEmails('a@example.com\n b@example.com, a@example.com;nope\n\n')).toEqual({
      valid: ['a@example.com', 'b@example.com'],
      invalid: ['nope'],
    })
  })
  it('gives nothing for nothing', () => {
    expect(parseEmails('  \n ')).toEqual({ valid: [], invalid: [] })
  })
})
