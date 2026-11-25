// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The to-do count in the sidebar next to 密钥与设备: how many personal secrets the resources I can use still
// need from me. The shell loads it once; the pages that change secrets set it again.

import { signal } from '@preact/signals'
import { api, type Schemas } from '../api/client'

export const missingSecrets = signal(0)

/** countMissing is how many of these secrets are not configured. */
export function countMissing(secrets: readonly Schemas['MySecret'][]): number {
  return secrets.filter((s) => !s.configured).length
}

/** loadMissingSecrets refreshes the count. A failure leaves it as it was: the count is a convenience. */
export function loadMissingSecrets() {
  api.GET('/api/v1/me/secrets').then(
    (res) => {
      if (res.data) missingSecrets.value = countMissing(res.data.items)
    },
    () => {},
  )
}
