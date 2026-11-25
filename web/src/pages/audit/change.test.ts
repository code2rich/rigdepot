// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import { describe, expect, it } from 'vitest'
import { changeText } from './change'

describe('the audit change summary', () => {
  it('shows a field that went from one value to another', () => {
    expect(changeText({ version: { from: 8, to: 9 }, files_changed: 1 }, 'replaced')).toBe(
      'version: 8 → 9 · files_changed: 1',
    )
  })

  it('never shows a secret, only that it was replaced', () => {
    expect(changeText({ password: 'replaced' }, '已更换（不记值）')).toBe('password: 已更换（不记值）')
  })

  it('lists arrays and keeps other objects readable', () => {
    expect(changeText({ stages: { from: ['design'], to: ['design', 'develop'] } }, 'r')).toBe(
      'stages: design → design, develop',
    )
    expect(changeText({ min_version: { from: '0.3.0', to: '0.4.0', extra: 1 } }, 'r')).toBe(
      'min_version: {"from":"0.3.0","to":"0.4.0","extra":1}',
    )
  })

  it('says nothing changed with a dash', () => {
    expect(changeText({}, 'r')).toBe('—')
    expect(changeText({ fields: [] }, 'r')).toBe('fields: —')
  })
})
