// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import { describe, expect, it } from 'vitest'
import { JOBS, focusFor, jobOf, latestPerProduct } from './jobs'

describe('focusFor', () => {
  it('keeps only the stages the server offers', () => {
    const dev = JOBS.find((j) => j.id === 'developer')
    expect(dev && focusFor(dev, [{ id: 'develop' }, { id: 'test' }])).toEqual(['develop'])
  })
})

describe('jobOf', () => {
  it('finds the job by its name and leaves free text alone', () => {
    const name = (key: string) => key.replace('apply.job.', 'Job ')
    expect(jobOf('Job tester', name)).toBe('tester')
    expect(jobOf('Something else', name)).toBe('')
    expect(jobOf('', name)).toBe('')
  })
})

describe('latestPerProduct', () => {
  it('keeps the newest request of each product', () => {
    const r = (code: string, at: string, status: string) => ({
      product: { code, name: code },
      created_at: at,
      status,
    })
    const got = latestPerProduct([
      r('shop', '2026-11-25T10:00:00Z', 'pending'),
      r('pay', '2026-11-20T10:00:00Z', 'rejected'),
      r('shop', '2026-11-20T10:00:00Z', 'rejected'),
    ])
    expect(got.map((x) => `${x.product.code}:${x.status}`)).toEqual(['shop:pending', 'pay:rejected'])
  })
})
