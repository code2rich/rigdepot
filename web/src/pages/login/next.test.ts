// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import { describe, expect, it } from 'vitest'
import { safeNext } from './next'

describe('safeNext', () => {
  it('follows paths inside the console', () => {
    expect(safeNext('/catalog')).toBe('/catalog')
    expect(safeNext('/device?code=KQ7M-2XHP')).toBe('/device?code=KQ7M-2XHP')
  })
  it('goes home for anything else', () => {
    for (const bad of [
      undefined,
      '',
      'catalog',
      '//example.com',
      '/\\example.com',
      'https://example.com',
      '/login',
      '/login?next=/x',
      '/a\nb',
    ]) {
      expect(safeNext(bad)).toBe('/')
    }
  })
})
