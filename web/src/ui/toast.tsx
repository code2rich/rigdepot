// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The short note at the bottom after something is done ("Saved"), as in the prototype's toast(). It is
// read out by screen readers and goes away by itself.

import { signal } from '@preact/signals'

const message = signal('')
let timer: ReturnType<typeof setTimeout> | undefined

/** toast shows a note for a few seconds, replacing the one before. */
export function toast(text: string) {
  clearTimeout(timer)
  message.value = text
  timer = setTimeout(() => {
    message.value = ''
  }, 3200)
}

/** Toast is where the notes appear. The shell renders it once; pages outside the shell (sign-in, setup) render their own. */
export function Toast() {
  return (
    <div role="status" aria-live="polite">
      {message.value && (
        <div class="toast">
          <i />
          {message.value}
        </div>
      )}
    </div>
  )
}
