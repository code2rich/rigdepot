// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// What the console knows about the server and the signed-in person, loaded once at start
// (/api/v1/meta, /api/v1/me) and shared by the shell and the pages.

import { computed, signal } from '@preact/signals'
import type { Schemas } from '../api/client'
import type { Role } from '../pages/registry'

export const meta = signal<Schemas['ServerMeta'] | null>(null)

/** The signed-in person; null when nobody is signed in. */
export const me = signal<Schemas['Me'] | null>(null)

/** roleOf derives the navigation role: admin, owner of at least one product, or member. */
export function roleOf(person: Schemas['Me']): Role {
  if (person.platform_admin) return 'admin'
  return person.memberships.some((m) => m.role === 'owner') ? 'owner' : 'member'
}

export const role = computed<Role>(() => (me.value ? roleOf(me.value) : 'member'))
