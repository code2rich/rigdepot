// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import { t } from '../i18n'
import { isDark, toggleTheme } from '../theme'
import { Icon } from '../ui/icons'

/** ThemeButton switches between light and dark; the icon shows the theme it switches to. */
export function ThemeButton() {
  const label = isDark.value ? t('shell.theme.to_light') : t('shell.theme.to_dark')
  return (
    <button type="button" class="btn ghost icon" onClick={toggleTheme} aria-label={label} title={label}>
      <Icon name={isDark.value ? 'sun' : 'moon'} />
    </button>
  )
}
