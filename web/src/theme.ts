// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Light and dark. The default follows the system; a choice made with the switch is kept in this browser
// (localStorage "theme") and applied before the first paint by the boot script in index.html.

import { computed, signal } from '@preact/signals'

export type Theme = 'light' | 'dark'

/** The key in localStorage; index.html reads the same key. */
export const THEME_KEY = 'theme'

function stored(): Theme | null {
  try {
    const v = localStorage.getItem(THEME_KEY)
    return v === 'light' || v === 'dark' ? v : null
  } catch {
    return null
  }
}

const media = typeof matchMedia === 'function' ? matchMedia('(prefers-color-scheme: dark)') : undefined

/** The theme picked by hand, or null to follow the system. */
export const chosenTheme = signal<Theme | null>(stored())
const systemDark = signal(media?.matches ?? false)
media?.addEventListener('change', (e) => {
  systemDark.value = e.matches
})

/** Whether the console shows the dark theme right now. */
export const isDark = computed(() => (chosenTheme.value ?? (systemDark.value ? 'dark' : 'light')) === 'dark')

/** setTheme applies a theme and remembers it in this browser. */
export function setTheme(theme: Theme) {
  chosenTheme.value = theme
  document.documentElement.dataset.theme = theme
  try {
    localStorage.setItem(THEME_KEY, theme)
  } catch {
    // Storage can be off (private windows); the choice then lasts for this page only.
  }
}

export function toggleTheme() {
  setTheme(isDark.value ? 'light' : 'dark')
}
