// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import { describe, expect, it } from 'vitest'
import { normalizeCode, osName } from './code'

describe('normalizeCode', () => {
  it('accepts the code typed in any way', () => {
    expect(normalizeCode('KQ7M-2XHP')).toBe('KQ7M-2XHP')
    expect(normalizeCode('kq7m2xhp')).toBe('KQ7M-2XHP')
    expect(normalizeCode(' kq7m 2xhp ')).toBe('KQ7M-2XHP')
  })
  it('refuses anything else', () => {
    for (const bad of [undefined, '', 'KQ7M', 'KQ7M-2XHP-1', 'KQ7M_2XHP', '../x/abcd'])
      expect(normalizeCode(bad)).toBe('')
  })
})

describe('osName', () => {
  it('names the systems people know', () => {
    expect(osName('darwin')).toBe('macOS')
    expect(osName('plan9')).toBe('plan9')
  })
})
