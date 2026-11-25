// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The short confirmation at the bottom of the screen after something was done ("已发布，大家下次同步时生效").
// toast() shows one; a page renders <Toasts /> once. Failures are not toasts: they stay next to what failed.

import { signal } from '@preact/signals'
import { createPortal } from 'preact/compat'

const current = signal<{ text: string; n: number } | null>(null)
let n = 0

/** toast shows a message for a few seconds. */
export function toast(text: string) {
  const mine = ++n
  current.value = { text, n: mine }
  setTimeout(() => {
    if (current.value?.n === mine) current.value = null
  }, 4200)
}

export function Toasts() {
  const now = current.value
  // In <body>, so that the page's transform does not move it (see overlay.tsx).
  return createPortal(
    <div role="status" aria-live="polite">
      {now && (
        <div key={now.n} class="toast">
          <i />
          {now.text}
        </div>
      )}
    </div>,
    document.body,
  )
}
