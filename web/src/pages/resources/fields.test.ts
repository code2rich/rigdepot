// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import { describe, expect, it } from 'vitest'
import type { Schemas } from '../../api/client'
import { changes, type Settings } from './fields'
import { matchesStatus } from './labels'

const base: Settings = {
  name: 'Design document',
  summary: 'Writes the design document.',
  audience: 'rd',
  stages: ['design'],
  scope: 'org',
  policy: 'recommended',
  when: '',
}

describe('saving a resource’s settings', () => {
  it('sends only the fields that changed, so the others do not become platform overrides', () => {
    expect(changes(base, { ...base })).toEqual({})
    expect(changes(base, { ...base, policy: 'required', scope: 'shop' })).toEqual({
      policy: 'required',
      scope: 'shop',
    })
  })

  it('ignores whitespace around text and compares stages in order', () => {
    expect(changes(base, { ...base, name: ' Design document ' })).toEqual({})
    expect(changes(base, { ...base, stages: ['design', 'develop'] })).toEqual({
      stages: ['design', 'develop'],
    })
    expect(changes(base, { ...base, stages: [] })).toEqual({ stages: [] })
  })
})

describe('the status filter', () => {
  const r = (over: Partial<Schemas['ResourceSummary']>): Schemas['ResourceSummary'] => ({
    code: 'x',
    kind: 'skill',
    name: 'X',
    stages: [],
    audience: 'rd',
    scope: 'org',
    policy: 'optional',
    origin: 'repo',
    status: 'published',
    version: 2,
    has_draft: false,
    checks: { blocking: 0, advisory: 0 },
    updated_at: '2026-11-25T08:00:00Z',
    ...over,
  })

  it('tells a published resource with a waiting update from a never-published draft', () => {
    expect(matchesStatus(r({ has_draft: true }), 'pending')).toBe(true)
    expect(matchesStatus(r({ status: 'draft', has_draft: true, version: undefined }), 'pending')).toBe(false)
    expect(matchesStatus(r({ status: 'draft', has_draft: true, version: undefined }), 'draft')).toBe(true)
  })

  it('finds what fails a must-fix check, whatever its status', () => {
    expect(matchesStatus(r({ checks: { blocking: 1, advisory: 0 } }), 'blocked')).toBe(true)
    expect(matchesStatus(r({ checks: { blocking: 0, advisory: 3 } }), 'blocked')).toBe(false)
    expect(matchesStatus(r({ status: 'disabled' }), 'published')).toBe(false)
  })
})
