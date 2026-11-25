// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The short confirmation at the bottom of the screen ("已保存"), as in the prototype. One at a time; it goes
// away after a few seconds. The shell renders it; anything calls toast().

import { signal } from '@preact/signals'

const message = signal('')
let timer: ReturnType<typeof setTimeout> | undefined

/** toast shows a message for a few seconds. */
export function toast(text: string) {
  clearTimeout(timer)
  message.value = text
  timer = setTimeout(() => {
    message.value = ''
  }, 3200)
}

/** Toast is the place the message appears; rendered once, by the shell. */
export function Toast() {
  if (!message.value) return null
  return (
    <div class="toast" role="status">
      <i />
      {message.value}
    </div>
  )
}
