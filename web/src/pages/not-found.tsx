// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import { t } from '../i18n'
import { href } from '../router/paths'
import { Empty } from '../ui/empty'

/** NotFound is any console path that is not a page. */
export function NotFound() {
  return (
    <div class="card">
      <Empty title={t('page.not_found.title')} why={t('page.not_found.why')}>
        <a class="btn" href={href('/')}>
          {t('page.not_found.home')}
        </a>
      </Empty>
    </div>
  )
}
