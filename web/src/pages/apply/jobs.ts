// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The jobs someone applying can pick, as in the prototype (JOBS in data.js), and the stages each one
// starts with. The job is saved as free text in the person's language; it decides defaults, never access.

import type { Schemas } from '../../api/client'
import type { MessageKey } from '../../i18n'

export interface Job {
  id: string
  label: MessageKey
  /** The stages picked for this job until the person changes them. */
  stages: readonly string[]
}

export const JOBS: readonly Job[] = [
  { id: 'product', label: 'apply.job.product', stages: ['requirement', 'design'] },
  { id: 'architect', label: 'apply.job.architect', stages: ['architecture', 'design'] },
  { id: 'developer', label: 'apply.job.developer', stages: ['design', 'develop'] },
  { id: 'tester', label: 'apply.job.tester', stages: ['test'] },
  { id: 'scm', label: 'apply.job.scm', stages: ['scm', 'release'] },
  { id: 'ops', label: 'apply.job.ops', stages: ['release', 'ops'] },
]

/** focusFor is a job's starting stages among the ones the server offers. */
export function focusFor(job: Job, stages: readonly Pick<Schemas['Stage'], 'id'>[]): string[] {
  return job.stages.filter((id) => stages.some((s) => s.id === id))
}

/** jobOf finds the job whose name is the saved text, or "" when it is none of them. */
export function jobOf(saved: string, name: (key: MessageKey) => string): string {
  return saved ? (JOBS.find((j) => name(j.label) === saved)?.id ?? '') : ''
}

/** latestPerProduct keeps the newest request for each product (the list comes newest first). */
export function latestPerProduct<R extends Pick<Schemas['JoinRequest'], 'product' | 'created_at'>>(
  requests: readonly R[],
): R[] {
  const seen = new Map<string, R>()
  for (const r of requests) {
    const had = seen.get(r.product.code)
    if (!had || had.created_at < r.created_at) seen.set(r.product.code, r)
  }
  return [...seen.values()]
}
